package instancelock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLockerSerializesSameInstance(t *testing.T) {
	locker := New()
	instanceID := uuid.New()

	release, err := locker.Acquire(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		releaseSecond, err := locker.Acquire(context.Background(), instanceID)
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		releaseSecond()
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire returned while the first was held")
	case <-time.After(50 * time.Millisecond):
	}

	release()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Acquire did not return after the first release")
	}
}

func TestLockerAllowsDifferentInstances(t *testing.T) {
	locker := New()
	firstID := uuid.New()
	secondID := uuid.New()

	releaseFirst, err := locker.Acquire(context.Background(), firstID)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	defer releaseFirst()

	acquired := make(chan struct{})
	go func() {
		release, err := locker.Acquire(context.Background(), secondID)
		if err != nil {
			t.Errorf("Acquire second: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("Acquire of a different instance blocked behind the first")
	}
}

func TestLockerAcquireHonorsContext(t *testing.T) {
	locker := New()
	instanceID := uuid.New()

	release, err := locker.Acquire(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	releaseWaiting, err := locker.Acquire(ctx, instanceID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire error = %v, want context.DeadlineExceeded", err)
	}
	if releaseWaiting != nil {
		t.Fatal("Acquire returned a release func for an unacquired lock")
	}

	release()

	// The canceled waiter must not have stolen the lock: it is free again.
	freeRelease, err := locker.Acquire(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Acquire after cancellation: %v", err)
	}
	freeRelease()
}

func TestLockerReleaseIsIdempotent(t *testing.T) {
	locker := New()
	instanceID := uuid.New()

	release, err := locker.Acquire(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondRelease, err := locker.Acquire(ctx, instanceID)
	if err != nil {
		t.Fatalf("Acquire after double release: %v", err)
	}
	secondRelease()
}

func TestLockerDropsReleasedInstances(t *testing.T) {
	locker := New()
	instanceID := uuid.New()

	release, err := locker.Acquire(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()

	locker.mu.Lock()
	defer locker.mu.Unlock()
	if len(locker.locks) != 0 {
		t.Errorf("locks kept %d entries after release, want 0", len(locker.locks))
	}
}
