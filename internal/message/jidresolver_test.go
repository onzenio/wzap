package message

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
)

// discardLogger returns a logger that drops every record.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeJIDCache is an in-memory storage.JIDCacheRepository that records every
// call and can force failures.
type fakeJIDCache struct {
	entries map[string]string
	getErr  error
	putErr  error
	gets    []string
	puts    []jidCachePut
}

// jidCachePut records one Put call.
type jidCachePut struct {
	phone     string
	jid       string
	expiresAt time.Time
}

func newFakeJIDCache() *fakeJIDCache {
	return &fakeJIDCache{entries: make(map[string]string)}
}

func (c *fakeJIDCache) Get(_ context.Context, phone string) (string, bool, error) {
	c.gets = append(c.gets, phone)
	if c.getErr != nil {
		return "", false, c.getErr
	}
	jid, ok := c.entries[phone]
	return jid, ok, nil
}

func (c *fakeJIDCache) Put(_ context.Context, phone, jid string, expiresAt time.Time) error {
	c.puts = append(c.puts, jidCachePut{phone: phone, jid: jid, expiresAt: expiresAt})
	if c.putErr != nil {
		return c.putErr
	}
	c.entries[phone] = jid
	return nil
}

func (c *fakeJIDCache) DeleteExpired(context.Context) (int64, error) { return 0, nil }

// fixedClock is a manually advanced clock for the cache TTL tests.
type fixedClock struct {
	at time.Time
}

func (c *fixedClock) now() time.Time          { return c.at }
func (c *fixedClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// newResolverFixture builds a resolver over a manager holding one session.
func newResolverFixture(t *testing.T) (*JIDResolver, uuid.UUID, *sessiontest.FakeSession, *fakeJIDCache) {
	t.Helper()
	id := uuid.New()
	manager := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	manager.Put(id, sess)
	cache := newFakeJIDCache()
	return NewJIDResolver(manager, cache, discardLogger()), id, sess, cache
}

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{name: "international digits", phone: "5547988359190", want: "5547988359190"},
		{name: "formatted international", phone: "+55 (47) 98835-9190", want: "5547988359190"},
		{name: "local digits", phone: "47988359190", want: "47988359190"},
		{name: "user jid", phone: "5547988359190@s.whatsapp.net", want: "5547988359190"},
		{name: "group jid", phone: "120363000000000000@g.us", want: "120363000000000000"},
		{name: "no digits", phone: "abc", want: ""},
		{name: "empty", phone: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePhone(tt.phone); got != tt.want {
				t.Errorf("NormalizePhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}

func TestBrazilianVariant(t *testing.T) {
	tests := []struct {
		name   string
		digits string
		want   string
	}{
		{name: "13 digits drops the ninth", digits: "5547988359190", want: "554788359190"},
		{name: "12 digit mobile gains the ninth", digits: "554788359190", want: "5547988359190"},
		{name: "12 digit prefix 6 gains the ninth", digits: "554763441234", want: "5547963441234"},
		{name: "12 digit fixed line has no variant", digits: "554733441234", want: ""},
		{name: "12 digit prefix 5 has no variant", digits: "554753441234", want: ""},
		{name: "13 digit without ninth has no variant", digits: "5547883591901", want: ""},
		{name: "no country code has no variant", digits: "47988359190", want: ""},
		{name: "short input has no variant", digits: "55478835", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := brazilianVariant(tt.digits); got != tt.want {
				t.Errorf("brazilianVariant(%q) = %q, want %q", tt.digits, got, tt.want)
			}
		})
	}
}

func TestResolveNormalizesAndCachesPositive(t *testing.T) {
	resolver, id, sess, cache := newResolverFixture(t)
	clock := &fixedClock{at: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	resolver.now = clock.now
	sess.OnWhatsApp = map[string]string{"5547988359190": "5547988359190@s.whatsapp.net"}

	jid, err := resolver.Resolve(context.Background(), id, "+55 (47) 98835-9190")
	if err != nil {
		t.Fatalf("Resolve error = %v, want nil", err)
	}
	if jid != "5547988359190@s.whatsapp.net" {
		t.Fatalf("Resolve jid = %q, want %q", jid, "5547988359190@s.whatsapp.net")
	}

	if got, err := resolver.Resolve(context.Background(), id, "5547988359190"); err != nil || got != jid {
		t.Fatalf("second Resolve = (%q, %v), want (%q, nil)", got, err, jid)
	}

	if calls := sess.IsOnWhatsAppCalls(); calls != 1 {
		t.Errorf("IsOnWhatsApp calls = %d, want 1 (the second resolution must hit the cache)", calls)
	}
	if len(cache.gets) != 2 || cache.gets[0] != "5547988359190" || cache.gets[1] != "5547988359190" {
		t.Errorf("cache Gets = %v, want the normalized number twice", cache.gets)
	}
	if len(cache.puts) != 1 {
		t.Fatalf("cache Puts = %d, want 1", len(cache.puts))
	}
	if put := cache.puts[0]; put.phone != "5547988359190" || put.jid != jid {
		t.Errorf("cache Put = %+v, want phone 5547988359190 and jid %q", put, jid)
	}
	if want := clock.at.Add(resolver.positiveTTL); !cache.puts[0].expiresAt.Equal(want) {
		t.Errorf("cache Put expiry = %v, want %v", cache.puts[0].expiresAt, want)
	}
}

func TestResolveTriesBrazilianAlternate(t *testing.T) {
	resolver, id, sess, cache := newResolverFixture(t)
	sess.OnWhatsApp = map[string]string{"554788359190": "554788359190@s.whatsapp.net"}

	jid, err := resolver.Resolve(context.Background(), id, "5547988359190")
	if err != nil {
		t.Fatalf("Resolve error = %v, want nil", err)
	}
	if jid != "554788359190@s.whatsapp.net" {
		t.Errorf("Resolve jid = %q, want the alternate form JID", jid)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Errorf("IsOnWhatsApp calls = %d, want 2 (original then alternate)", calls)
	}
	if len(cache.puts) != 1 || cache.puts[0].phone != "5547988359190" {
		t.Errorf("cache Puts = %+v, want one entry keyed by the requested number", cache.puts)
	}
}

func TestResolveTriesNinthDigitVariant(t *testing.T) {
	resolver, id, sess, _ := newResolverFixture(t)
	sess.OnWhatsApp = map[string]string{"5547988359190": "5547988359190@s.whatsapp.net"}

	jid, err := resolver.Resolve(context.Background(), id, "554788359190")
	if err != nil {
		t.Fatalf("Resolve error = %v, want nil", err)
	}
	if jid != "5547988359190@s.whatsapp.net" {
		t.Errorf("Resolve jid = %q, want the ninth-digit variant JID", jid)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Errorf("IsOnWhatsApp calls = %d, want 2 (original then variant)", calls)
	}
}

func TestResolveFixedLineSkipsVariant(t *testing.T) {
	resolver, id, sess, _ := newResolverFixture(t)

	_, err := resolver.Resolve(context.Background(), id, "554733441234")
	if !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("Resolve error = %v, want ErrNumberNotFound", err)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 1 {
		t.Errorf("IsOnWhatsApp calls = %d, want 1 (fixed lines have no variant)", calls)
	}
}

func TestResolveNotFoundReturnsSentinel(t *testing.T) {
	resolver, id, sess, _ := newResolverFixture(t)

	_, err := resolver.Resolve(context.Background(), id, "5547988359190")
	if !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("Resolve error = %v, want ErrNumberNotFound", err)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Errorf("IsOnWhatsApp calls = %d, want 2 (original and alternate)", calls)
	}
}

func TestResolvePositiveCacheSkipsSession(t *testing.T) {
	cache := newFakeJIDCache()
	cache.entries["5547988359190"] = "5547988359190@s.whatsapp.net"
	manager := sessiontest.New(nil)
	resolver := NewJIDResolver(manager, cache, discardLogger())

	jid, err := resolver.Resolve(context.Background(), uuid.New(), "5547988359190")
	if err != nil {
		t.Fatalf("Resolve error = %v, want nil from the cache without a session", err)
	}
	if jid != "5547988359190@s.whatsapp.net" {
		t.Errorf("Resolve jid = %q, want the cached JID", jid)
	}
	if len(cache.puts) != 0 {
		t.Errorf("cache Puts = %d, want none on a cache hit", len(cache.puts))
	}
}

func TestResolveNegativeCacheRespectsTTL(t *testing.T) {
	resolver, id, sess, _ := newResolverFixture(t)
	clock := &fixedClock{at: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	resolver.now = clock.now
	resolver.negativeTTL = time.Minute

	for i := 0; i < 2; i++ {
		if _, err := resolver.Resolve(context.Background(), id, "+55 (47) 98835-9190"); !errors.Is(err, ErrNumberNotFound) {
			t.Fatalf("Resolve #%d error = %v, want ErrNumberNotFound", i, err)
		}
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Fatalf("IsOnWhatsApp calls = %d, want 2 (the second lookup must hit the negative cache)", calls)
	}
	// The alternate form shares the negative entry: it was queried already.
	if _, err := resolver.Resolve(context.Background(), id, "554788359190"); !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("alternate Resolve error = %v, want ErrNumberNotFound", err)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Fatalf("IsOnWhatsApp calls = %d, want 2 for both spellings", calls)
	}

	clock.advance(time.Minute - time.Second)
	if _, err := resolver.Resolve(context.Background(), id, "5547988359190"); !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("Resolve before expiry error = %v, want ErrNumberNotFound", err)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 2 {
		t.Fatalf("IsOnWhatsApp calls = %d, want 2 before the negative TTL elapses", calls)
	}

	clock.advance(time.Second)
	if _, err := resolver.Resolve(context.Background(), id, "5547988359190"); !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("Resolve after expiry error = %v, want ErrNumberNotFound", err)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 4 {
		t.Errorf("IsOnWhatsApp calls = %d, want 4 after the negative TTL elapses", calls)
	}
}

func TestResolveNegativeCacheSweepsExpiredEntries(t *testing.T) {
	resolver, id, _, _ := newResolverFixture(t)
	clock := &fixedClock{at: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	resolver.now = clock.now

	if _, err := resolver.Resolve(context.Background(), id, "5547988359190"); !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("first Resolve error = %v, want ErrNumberNotFound", err)
	}
	clock.advance(2 * resolver.negativeTTL)
	if _, err := resolver.Resolve(context.Background(), id, "554711111111"); !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("second Resolve error = %v, want ErrNumberNotFound", err)
	}

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.negative) != 1 {
		t.Errorf("negative cache holds %d entries after the sweep, want 1", len(resolver.negative))
	}
	if _, ok := resolver.negative["554711111111"]; !ok {
		t.Error("the fresh negative entry was swept along with the expired ones")
	}
}

func TestResolvePassesFormedJIDThrough(t *testing.T) {
	resolver, id, sess, cache := newResolverFixture(t)

	jid, err := resolver.Resolve(context.Background(), id, "120363000000000000@g.us")
	if err != nil {
		t.Fatalf("Resolve error = %v, want nil", err)
	}
	if jid != "120363000000000000@g.us" {
		t.Errorf("Resolve jid = %q, want the input JID unchanged", jid)
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 0 {
		t.Errorf("IsOnWhatsApp calls = %d, want none for a formed JID", calls)
	}
	if len(cache.gets) != 0 {
		t.Errorf("cache Gets = %v, want none for a formed JID", cache.gets)
	}
}

func TestResolveRejectsInvalidPhone(t *testing.T) {
	resolver, id, sess, cache := newResolverFixture(t)

	for _, phone := range []string{"abc", "   ", "+-()"} {
		if _, err := resolver.Resolve(context.Background(), id, phone); !errors.Is(err, ErrNumberNotFound) {
			t.Errorf("Resolve(%q) error = %v, want ErrNumberNotFound", phone, err)
		}
	}
	if calls := sess.IsOnWhatsAppCalls(); calls != 0 {
		t.Errorf("IsOnWhatsApp calls = %d, want none for invalid numbers", calls)
	}
	if len(cache.gets) != 0 {
		t.Errorf("cache Gets = %v, want none for invalid numbers", cache.gets)
	}
}

func TestResolveUnavailableWithoutSession(t *testing.T) {
	resolver := NewJIDResolver(sessiontest.New(nil), newFakeJIDCache(), discardLogger())

	_, err := resolver.Resolve(context.Background(), uuid.New(), "5547988359190")
	if !errors.Is(err, ErrResolverUnavailable) {
		t.Errorf("Resolve error = %v, want ErrResolverUnavailable", err)
	}
}

func TestResolveUnavailableOnSessionFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "not connected", err: fmt.Errorf("%w: socket closed", session.ErrNotConnected)},
		{name: "transient", err: fmt.Errorf("%w: lookup timed out", session.ErrTransient)},
		{name: "unknown", err: errors.New("boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, id, sess, _ := newResolverFixture(t)
			sess.IsOnWhatsAppErr = tt.err

			_, err := resolver.Resolve(context.Background(), id, "5547988359190")
			if !errors.Is(err, ErrResolverUnavailable) {
				t.Errorf("Resolve error = %v, want ErrResolverUnavailable", err)
			}
		})
	}
}

func TestResolveCacheFailuresAreNotFatal(t *testing.T) {
	tests := []struct {
		name  string
		setUp func(*fakeJIDCache)
	}{
		{name: "read failure", setUp: func(c *fakeJIDCache) { c.getErr = errors.New("database down") }},
		{name: "write failure", setUp: func(c *fakeJIDCache) { c.putErr = errors.New("database down") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, id, sess, cache := newResolverFixture(t)
			sess.OnWhatsApp = map[string]string{"5547988359190": "5547988359190@s.whatsapp.net"}
			tt.setUp(cache)

			jid, err := resolver.Resolve(context.Background(), id, "5547988359190")
			if err != nil {
				t.Fatalf("Resolve error = %v, want the session result despite the cache failure", err)
			}
			if jid != "5547988359190@s.whatsapp.net" {
				t.Errorf("Resolve jid = %q, want the session JID", jid)
			}
		})
	}
}
