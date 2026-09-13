package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeExpiredDeleter records every DeleteExpired call.
type fakeExpiredDeleter struct {
	calls   int
	removed int
	err     error
	nows    []time.Time
}

func (f *fakeExpiredDeleter) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	f.calls++
	f.nows = append(f.nows, now)
	return f.removed, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCleanerRunCleansUntilCanceled(t *testing.T) {
	deleter := &fakeExpiredDeleter{removed: 3}
	cleaner := NewCleaner(deleter, discardLogger())
	cleaner.interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaner.sleep = func(sleepCtx context.Context, _ time.Duration) error {
		if deleter.calls >= 2 {
			cancel()
		}
		return sleepCtx.Err()
	}

	cleaner.Run(ctx)

	if deleter.calls != 2 {
		t.Errorf("DeleteExpired calls = %d, want 2", deleter.calls)
	}
	for i, now := range deleter.nows {
		if now.IsZero() {
			t.Errorf("DeleteExpired call %d received a zero time", i)
		}
	}
}

func TestCleanerRunContinuesAfterError(t *testing.T) {
	deleter := &fakeExpiredDeleter{err: errors.New("delete failed")}
	cleaner := NewCleaner(deleter, discardLogger())
	cleaner.interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaner.sleep = func(sleepCtx context.Context, _ time.Duration) error {
		if deleter.calls >= 2 {
			cancel()
		}
		return sleepCtx.Err()
	}

	cleaner.Run(ctx)

	if deleter.calls != 2 {
		t.Errorf("DeleteExpired calls = %d, want 2 (an error must not stop the cleaner)", deleter.calls)
	}
}

func TestCleanerRunStopsOnCanceledContext(t *testing.T) {
	deleter := &fakeExpiredDeleter{}
	cleaner := NewCleaner(deleter, discardLogger())
	cleaner.interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleaner.sleep = func(sleepCtx context.Context, _ time.Duration) error {
		return sleepCtx.Err()
	}

	cleaner.Run(ctx)

	if deleter.calls != 0 {
		t.Errorf("DeleteExpired calls = %d, want 0 (a canceled context must not touch the database)", deleter.calls)
	}
}
