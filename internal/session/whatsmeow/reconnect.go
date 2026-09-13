package whatsmeow

import (
	"context"
	"math/rand/v2"
	"time"

	"wzap/internal/session"
)

const (
	// reconnectBaseDelay is the first wait of the auto-reconnect backoff.
	reconnectBaseDelay = 2 * time.Second
	// reconnectMaxDelay caps the auto-reconnect backoff.
	reconnectMaxDelay = 2 * time.Minute
)

// sleepFunc waits for d or returns the context error when the wait is
// cancelled. It is replaceable so tests can assert the backoff without
// wall-clock sleeping.
type sleepFunc func(ctx context.Context, d time.Duration) error

// reconnectPolicy computes the wait before each auto-reconnect attempt:
// exponential from base up to max, with jitter applied inside the window.
type reconnectPolicy struct {
	base   time.Duration
	max    time.Duration
	jitter func(time.Duration) time.Duration
}

// delay returns the wait before the zero-based attempt.
func (p reconnectPolicy) delay(attempt int) time.Duration {
	wait := p.base
	for i := 0; i < attempt && wait < p.max; i++ {
		wait *= 2
	}
	if wait > p.max {
		wait = p.max
	}
	if p.jitter == nil {
		return wait
	}
	return p.jitter(wait)
}

// defaultJitter spreads a wait over [d/2, d] so reconnecting instances do not
// stampede the server while the wait still grows with every attempt.
func defaultJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// reconnectRun is one execution of the reconnect loop. Keeping the cancel
// handle in a value the loop can compare makes sure a finished loop never
// clears the handle of a newer one.
type reconnectRun struct {
	cancel context.CancelFunc
}

// scheduleReconnect starts the auto-reconnect loop unless one is already
// running, the session has no credentials to reconnect with or it is in a
// terminal state. The eligibility check and the registration share the lock,
// so a concurrent terminal transition either prevents the loop or cancels it.
func (s *instanceSession) scheduleReconnect() {
	s.mu.Lock()
	if !s.paired || s.terminal || s.reconnectRun != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &reconnectRun{cancel: cancel}
	s.reconnectRun = run
	s.mu.Unlock()

	go s.reconnectLoop(ctx, run)
}

// cancelReconnect stops a pending auto-reconnect loop, if any.
func (s *instanceSession) cancelReconnect() {
	s.mu.Lock()
	run := s.reconnectRun
	s.reconnectRun = nil
	s.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// reconnectLoop retries bringing the session online with growing waits until it
// succeeds or a terminal transition cancels the loop.
func (s *instanceSession) reconnectLoop(ctx context.Context, run *reconnectRun) {
	defer func() {
		s.mu.Lock()
		if s.reconnectRun == run {
			s.reconnectRun = nil
		}
		s.mu.Unlock()
	}()

	for attempt := 0; ; attempt++ {
		if err := s.sleep(ctx, s.backoff.delay(attempt)); err != nil {
			return
		}
		if err := s.reconnectFn(ctx); err != nil {
			s.log.Warn("reconnect attempt failed",
				"instance_id", s.instanceID, "attempt", attempt+1, "error", err)
			continue
		}
		return
	}
}

// applyConnectionPolicy reacts to a status transition: a transient drop
// (disconnected without a reason) schedules a retry loop; every other
// transition either recovered or is terminal, so it cancels a pending loop.
func (s *instanceSession) applyConnectionPolicy(status session.Status, reason string) {
	if status == session.StatusDisconnected && reason == "" {
		s.scheduleReconnect()
		return
	}
	s.cancelReconnect()
}
