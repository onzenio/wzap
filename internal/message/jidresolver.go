// Package message resolves message recipients and manages the outbound message
// lifecycle.
package message

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wzap/internal/session"
	"wzap/internal/storage"
)

// Errors reported by the resolver and mapped to HTTP results by the handler
// layer.
var (
	// ErrNumberNotFound reports that the phone is malformed or that it is not
	// registered on WhatsApp.
	ErrNumberNotFound = errors.New("number not found")
	// ErrResolverUnavailable reports that the lookup could not be attempted,
	// for example because the instance has no session or its connection failed.
	ErrResolverUnavailable = errors.New("jid resolver unavailable")
)

const (
	// positiveJIDTTL is how long a confirmed JID stays in the persistent cache.
	positiveJIDTTL = 24 * time.Hour
	// negativeJIDTTL is how long a miss blocks a new lookup of the same number.
	// It is short on purpose: the number can be registered later and a failed
	// variant probe must not poison the number for long.
	negativeJIDTTL = time.Minute
)

// JIDResolver resolves phone numbers to canonical WhatsApp JIDs, applying the
// Brazilian 9th-digit rule. Confirmed resolutions are cached in the persistent
// JID cache; misses are remembered in memory for a short TTL so a number that
// is not on WhatsApp does not reach the backend on every request.
type JIDResolver struct {
	sessions session.Manager
	cache    storage.JIDCacheRepository
	log      *slog.Logger

	// now, positiveTTL and negativeTTL are fields so tests can control time.
	now         func() time.Time
	positiveTTL time.Duration
	negativeTTL time.Duration

	mu        sync.Mutex
	negative  map[string]time.Time
	lastSweep time.Time
}

// NewJIDResolver builds the resolver over the session manager and the positive
// JID cache.
func NewJIDResolver(sessions session.Manager, cache storage.JIDCacheRepository, log *slog.Logger) *JIDResolver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &JIDResolver{
		sessions:    sessions,
		cache:       cache,
		log:         log,
		now:         time.Now,
		positiveTTL: positiveJIDTTL,
		negativeTTL: negativeJIDTTL,
		negative:    make(map[string]time.Time),
	}
}

// Resolve returns the canonical JID of phone for the instance. A value that
// already contains '@' is returned unchanged. It answers ErrNumberNotFound
// when the phone is malformed or not registered, and ErrResolverUnavailable
// when the instance has no usable session.
func (r *JIDResolver) Resolve(ctx context.Context, instanceID uuid.UUID, phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", fmt.Errorf("%w: empty phone", ErrNumberNotFound)
	}
	if strings.Contains(phone, "@") {
		return phone, nil
	}

	digits := NormalizePhone(phone)
	if digits == "" {
		return "", fmt.Errorf("%w: %q has no digits", ErrNumberNotFound, phone)
	}

	if r.isNegative(digits) {
		return "", fmt.Errorf("%w: %s (negative cache)", ErrNumberNotFound, digits)
	}

	if jid, ok, err := r.cache.Get(ctx, digits); err != nil {
		r.log.Warn("jid cache read failed", "phone", digits, "error", err)
	} else if ok {
		return jid, nil
	}

	sess, ok := r.sessions.Get(instanceID)
	if !ok {
		return "", fmt.Errorf("%w: instance %s has no session", ErrResolverUnavailable, instanceID)
	}

	jid, err := r.lookup(ctx, sess, digits)
	if err != nil {
		return "", err
	}
	if jid == "" {
		// The alternate form was probed as part of this resolution, so its miss
		// is cached too.
		r.rememberNegative(digits)
		if variant := brazilianVariant(digits); variant != "" {
			r.rememberNegative(variant)
		}
		return "", fmt.Errorf("%w: %s", ErrNumberNotFound, digits)
	}

	if err := r.cache.Put(ctx, digits, jid, r.now().Add(r.positiveTTL)); err != nil {
		r.log.Warn("jid cache write failed", "phone", digits, "error", err)
	}
	return jid, nil
}

// lookup queries the session for the number and, when the Brazilian rule
// applies, for its alternate form.
func (r *JIDResolver) lookup(ctx context.Context, sess session.Session, digits string) (string, error) {
	jid, ok, err := sess.IsOnWhatsApp(ctx, digits)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrResolverUnavailable, err)
	}
	if ok && jid != "" {
		return jid, nil
	}

	variant := brazilianVariant(digits)
	if variant == "" {
		return "", nil
	}
	jid, ok, err = sess.IsOnWhatsApp(ctx, variant)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrResolverUnavailable, err)
	}
	if ok && jid != "" {
		return jid, nil
	}
	return "", nil
}

// isNegative reports whether phone was recently resolved as absent, dropping
// the entry once it expired.
func (r *JIDResolver) isNegative(phone string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	expiresAt, ok := r.negative[phone]
	if !ok {
		return false
	}
	if !r.now().Before(expiresAt) {
		delete(r.negative, phone)
		return false
	}
	return true
}

// rememberNegative stores a miss for phone until the negative TTL elapses. It
// sweeps expired entries once per TTL so the map cannot grow without bound.
func (r *JIDResolver) rememberNegative(phone string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if now.Sub(r.lastSweep) >= r.negativeTTL {
		for key, expiresAt := range r.negative {
			if !now.Before(expiresAt) {
				delete(r.negative, key)
			}
		}
		r.lastSweep = now
	}
	r.negative[phone] = now.Add(r.negativeTTL)
}

// NormalizePhone keeps only the digits of phone, ignoring the server of a JID.
func NormalizePhone(phone string) string {
	if user, _, found := strings.Cut(phone, "@"); found {
		phone = user
	}
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
}

// brazilianVariant returns the alternate form of a Brazilian phone number: a
// 55-prefixed 13-digit number (DDD + 9 + 8) is retried without the 9th digit,
// and a 55-prefixed 12-digit mobile number (first digit 6-9) is retried with
// it. Fixed lines (first digit 2-5) and foreign numbers have no alternate.
func brazilianVariant(digits string) string {
	if !strings.HasPrefix(digits, "55") {
		return ""
	}

	switch len(digits) {
	case 13:
		if digits[4] != '9' {
			return ""
		}
		return digits[:4] + digits[5:]
	case 12:
		first := digits[4]
		if first < '6' || first > '9' {
			return ""
		}
		return digits[:4] + "9" + digits[4:]
	default:
		return ""
	}
}
