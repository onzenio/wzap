// Package instancelock serializes the WhatsApp-facing operations of a single
// instance while keeping different instances free to run in parallel.
package instancelock

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// Locker holds one mutual exclusion per instance. Entries are dropped once the
// last holder or waiter releases, so a long-running process does not grow the
// map with deleted instances.
type Locker struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*lockEntry
}

// lockEntry is the per-instance semaphore plus the number of holders and
// waiters that still reference it.
type lockEntry struct {
	sem  chan struct{}
	refs int
}

// New returns an empty Locker. The zero value is also usable.
func New() *Locker {
	return &Locker{locks: make(map[uuid.UUID]*lockEntry)}
}

// Acquire blocks until instanceID is free or ctx is done. On success it
// returns an idempotent release function; on cancellation it returns the
// context error and no release function.
func (l *Locker) Acquire(ctx context.Context, instanceID uuid.UUID) (func(), error) {
	entry := l.retain(instanceID)

	select {
	case entry.sem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-entry.sem
				l.release(instanceID)
			})
		}, nil
	case <-ctx.Done():
		l.release(instanceID)
		return nil, ctx.Err()
	}
}

// retain returns the entry of instanceID, creating it when absent, and counts
// the caller as an owner.
func (l *Locker) retain(instanceID uuid.UUID) *lockEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[uuid.UUID]*lockEntry)
	}
	entry, ok := l.locks[instanceID]
	if !ok {
		entry = &lockEntry{sem: make(chan struct{}, 1)}
		l.locks[instanceID] = entry
	}
	entry.refs++
	return entry
}

// release drops one owner of instanceID, deleting the entry with the last one.
func (l *Locker) release(instanceID uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.locks[instanceID]
	if !ok {
		return
	}
	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, instanceID)
	}
}
