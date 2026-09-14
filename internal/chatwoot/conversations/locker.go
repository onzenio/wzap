package conversations

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// defaultLockTTL bounds how long a sender key stays held: an unreleased
	// lock expires instead of wedging the sender forever.
	defaultLockTTL = 30 * time.Second
	// defaultLockPoll is the wait interval while another holder owns the key.
	defaultLockPoll = 300 * time.Millisecond
	// defaultLockTimeout caps the wait for a contended key.
	defaultLockTimeout = 5 * time.Second
)

// errLockTimeout reports a sender key held past the acquisition timeout.
var errLockTimeout = errors.New("conversations: sender lock timeout")

// locker serializes work per sender key, mirroring instancelock with string
// keys and a TTL: entries expire so a crashed holder cannot block a sender,
// and release only clears its own token so an expired handover is safe.
// The locker only guarantees mutual exclusion; single-execution convergence
// (one conversation under concurrency) is owned by Resolve with lock +
// double-check (see TestResolveConvergesConcurrentSenders).
type locker struct {
	mu      sync.Mutex
	held    map[string]time.Time
	ttl     time.Duration
	poll    time.Duration
	timeout time.Duration
	now     func() time.Time
}

// newLocker returns a locker with the production timings.
func newLocker() *locker {
	return &locker{
		held:    make(map[string]time.Time),
		ttl:     defaultLockTTL,
		poll:    defaultLockPoll,
		timeout: defaultLockTimeout,
		now:     time.Now,
	}
}

// acquire blocks until key is free, ctx ends, or the timeout elapses. On
// success it returns an idempotent release function.
func (l *locker) acquire(ctx context.Context, key string) (func(), error) {
	poll := l.poll
	if poll <= 0 {
		poll = defaultLockPoll
	}
	timeout := l.timeout
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	deadline := l.now().Add(timeout)
	for {
		if release, ok := l.tryAcquire(key); ok {
			return release, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !l.now().Before(deadline) {
			return nil, errLockTimeout
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// tryAcquire takes key when free or expired, returning a release that only
// clears its own token.
func (l *locker) tryAcquire(key string) (func(), bool) {
	ttl := l.ttl
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if expiresAt, ok := l.held[key]; ok && now.Before(expiresAt) {
		return nil, false
	}
	token := now.Add(ttl)
	l.held[key] = token
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if expiresAt, ok := l.held[key]; ok && expiresAt.Equal(token) {
				delete(l.held, key)
			}
		})
	}, true
}
