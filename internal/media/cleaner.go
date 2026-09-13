package media

import (
	"context"
	"log/slog"
	"time"
)

// defaultCleanerInterval is how often the cleaner removes expired media.
const defaultCleanerInterval = time.Minute

// ExpiredDeleter removes the expired media and reports how many records were
// removed. *Storage implements it.
type ExpiredDeleter interface {
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// The storage satisfies the cleaner contract; the assertion catches signature
// drift at build time.
var _ ExpiredDeleter = (*Storage)(nil)

// Cleaner removes expired media files and rows periodically.
type Cleaner struct {
	storage  ExpiredDeleter
	log      *slog.Logger
	interval time.Duration
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

// NewCleaner returns a cleaner over storage that cleans once at boot and then
// every defaultCleanerInterval. A nil logger falls back to the default one.
func NewCleaner(storage ExpiredDeleter, log *slog.Logger) *Cleaner {
	if log == nil {
		log = slog.Default()
	}
	return &Cleaner{
		storage:  storage,
		log:      log,
		interval: defaultCleanerInterval,
		now:      time.Now,
		sleep:    sleepContext,
	}
}

// Run removes expired media until ctx is canceled. A failed pass is logged and
// retried after the interval; only cancellation stops the loop.
func (c *Cleaner) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		c.cleanup(ctx)
		if c.sleep(ctx, c.interval) != nil {
			return
		}
	}
}

// cleanup runs one pass, logging the outcome instead of aborting the loop.
func (c *Cleaner) cleanup(ctx context.Context) {
	removed, err := c.storage.DeleteExpired(ctx, c.now())
	if err != nil {
		c.log.WarnContext(ctx, "clean expired media", "error", err)
	}
	if removed > 0 {
		c.log.InfoContext(ctx, "cleaned expired media", "removed", removed)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
