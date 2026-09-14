package conversations

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockerSerializesSameKey pins the locker's real contract: mutual
// exclusion on one key with every holder completing in turn (all 20 runs
// execute serially, never overlapping). Single-execution convergence (20
// goroutines → 1 CreateConversation) is NOT a locker property; it is owned
// by Resolve with lock + double-check, see TestResolveConvergesConcurrentSenders.
func TestLockerSerializesSameKey(t *testing.T) {
	l := newLocker()
	l.poll = 5 * time.Millisecond
	const runners = 20
	var active, maxActive, executions atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.acquire(context.Background(), "instance|sender")
			if err != nil {
				t.Errorf("acquire = %v, want nil", err)
				return
			}
			defer release()
			current := active.Add(1)
			for {
				max := maxActive.Load()
				if current <= max || maxActive.CompareAndSwap(max, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			executions.Add(1)
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Errorf("max concurrent holders = %d, want 1", got)
	}
	if got := executions.Load(); got != runners {
		t.Errorf("executions = %d, want %d serialized runs", got, runners)
	}
}

// TestLockerDistinctKeysDoNotBlock ensures a held key never blocks another key.
func TestLockerDistinctKeysDoNotBlock(t *testing.T) {
	l := newLocker()
	release, err := l.acquire(context.Background(), "instance|alice")
	if err != nil {
		t.Fatalf("acquire alice = %v, want nil", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	releaseBob, err := l.acquire(ctx, "instance|bob")
	if err != nil {
		t.Fatalf("acquire bob while alice held = %v, want nil (no cross-blocking)", err)
	}
	releaseBob()
}

// TestLockerExpiryReleases ensures an unreleased lock becomes acquirable
// after its TTL, so a crashed holder cannot wedge a sender forever.
func TestLockerExpiryReleases(t *testing.T) {
	l := newLocker()
	l.ttl = 30 * time.Millisecond
	l.poll = time.Millisecond
	if _, err := l.acquire(context.Background(), "instance|sender"); err != nil {
		t.Fatalf("first acquire = %v, want nil", err)
	}
	// No release on purpose: the TTL must free the key.
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := l.acquire(ctx, "instance|sender")
	if err != nil {
		t.Fatalf("acquire after expiry = %v, want nil", err)
	}
	release()
}
