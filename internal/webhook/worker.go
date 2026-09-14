package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/model"
	"wzap/internal/storage"
)

const (
	// BufferSize bounds the webhook dispatch queue. Dispatch is non-blocking:
	// once the buffer is full further events are dropped with a counter
	// instead of slowing the NATS path.
	BufferSize = 1000
	// MaxAttempts is the total number of delivery attempts per job, sleeps
	// between attempts included. After the last failure the job is
	// dead-lettered in the log.
	MaxAttempts = 8
	// webhookBackoffBase is the first retry sleep; it doubles per attempt.
	webhookBackoffBase = time.Second
	// webhookBackoffMax caps every retry sleep.
	webhookBackoffMax = 5 * time.Minute
	// MaxInflight caps concurrent deliveries: cada handle segura um POST de
	// até 5s contra endpoint externo, então 16 concorrentes limitam FDs e
	// memória sob burst sem serializar o retry.
	MaxInflight = 16
)

// DeliverFunc delivers payload to url with the vigente instance key. It
// matches Deliver so production passes Deliver directly and tests inject
// scripted endpoints.
type DeliverFunc func(ctx context.Context, url, key string, payload []byte) error

// InstanceLoader reads the instance row fresh on every delivery attempt, so a
// disabled webhook, a cleared URL or a changed subscription takes effect
// immediately, even mid-backoff. storage.InstanceRepository implements it.
type InstanceLoader interface {
	Get(ctx context.Context, id uuid.UUID) (*model.Instance, error)
}

// job is one queued webhook delivery: the envelope plus the fields the
// worker needs without unmarshaling the payload again.
type job struct {
	envelope   events.Envelope
	instanceID uuid.UUID
	eventID    uuid.UUID
	eventType  string
}

// Worker delivers event envelopes to per-instance webhook endpoints with
// retry and dead-letter. It is best-effort by design: the NATS outbox stays
// the source of truth and the worker never blocks the event path.
type Worker struct {
	loader        InstanceLoader
	keys          *KeyCache
	deliver       DeliverFunc
	maxMediaBytes int64
	log           *slog.Logger
	// Sleep waits out the backoff between attempts. It is a field à la
	// Humanizer so tests inject an instant sleep; production uses a
	// context-aware real sleep.
	Sleep func(ctx context.Context, d time.Duration) error

	queue   chan job
	dropped atomic.Int64
	// sem capa deliveries concorrentes (MaxInflight). O Run adquire antes
	// de destacar o handle e libera no fim, então a fila continua
	// bufferizando enquanto o paralelismo externo fica limitado.
	sem chan struct{}
}

// NewWorker builds a webhook worker over the instance loader, the process
// key cache, the deliver function and the media limit bounding the envelope
// event. A nil logger falls back to the default one and a nil cache to a
// fresh empty one.
func NewWorker(loader InstanceLoader, keys *KeyCache, deliver DeliverFunc, maxMediaBytes int64, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if keys == nil {
		keys = NewKeyCache()
	}
	if deliver == nil {
		deliver = Deliver
	}
	return &Worker{
		loader:        loader,
		keys:          keys,
		deliver:       deliver,
		maxMediaBytes: maxMediaBytes,
		log:           log,
		Sleep:         sleepContext,
		queue:         make(chan job, BufferSize),
		sem:           make(chan struct{}, MaxInflight),
	}
}

// Dropped reports how many events were dropped on a full queue so far.
func (w *Worker) Dropped() int64 {
	return w.dropped.Load()
}

// Fanout decorates inner with the webhook fan-out: every Write enqueues the
// {instanceID, envelope} NON-BLOCKINGLY for the worker and always calls the
// inner Write, so the webhook never breaks the NATS path and NATS errors
// never break the webhook (the job is already queued when inner fails).
//
// The NATS relay replays from the DB outbox, NOT through Writer, so fanning
// out here delivers every event exactly once per Write with no double
// delivery.
func (w *Worker) Fanout(inner events.Writer) events.Writer {
	return &fanoutWriter{worker: w, inner: inner}
}

// fanoutWriter is the Writer decorator fanning events out to the worker.
type fanoutWriter struct {
	worker *Worker
	inner  events.Writer
}

func (f *fanoutWriter) Write(ctx context.Context, subject string, env events.Envelope) error {
	f.worker.dispatch(env)
	return f.inner.Write(ctx, subject, env)
}

// dispatch queues env for the worker, dropping it with a counter when the
// buffer is full. It never blocks and never fails: the caller still runs the
// inner write.
func (w *Worker) dispatch(env events.Envelope) {
	j := job{envelope: env, instanceID: env.InstanceID, eventID: env.EventID, eventType: env.Type}
	select {
	case w.queue <- j:
	default:
		dropped := w.dropped.Add(1)
		w.log.Warn("webhook queue full, dropping event",
			"instance_id", env.InstanceID, "event_id", env.EventID, "event_type", env.Type, "dropped", dropped)
	}
}

// Run dequeues jobs until ctx is cancelled. Every job is handled in its OWN
// goroutine, so one failing delivery never blocks the following ones, capped
// at MaxInflight concurrent handles by the semaphore. On cancel the pending
// queue is dropped with a count log — NO drain: webhooks are best-effort and
// the shared shutdown budget belongs to the relay, while the NATS outbox
// remains the source of truth.
func (w *Worker) Run(ctx context.Context) {
	sem := w.sem
	if sem == nil {
		sem = make(chan struct{}, MaxInflight)
	}
	for {
		select {
		case <-ctx.Done():
			w.log.InfoContext(ctx, "webhook worker stopped", "pending", len(w.queue))
			return
		case j := <-w.queue:
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				w.log.InfoContext(ctx, "webhook worker stopped", "pending", len(w.queue)+1)
				return
			}
			go func(job job) {
				defer func() { <-sem }()
				w.handle(ctx, job)
			}(j)
		}
	}
}

// handle delivers one job with the retry schedule, dead-lettering it after
// the last failure. Cancellation aborts in-flight attempts: deliver and
// sleep both honor ctx.
func (w *Worker) handle(ctx context.Context, j job) {
	env := j.envelope
	// Defensive re-cut against maxMediaBytes: producers already trim the raw
	// before Write, so this is a no-op for them (the cut is idempotent), and
	// it bounds anything a future producer sets untrimmed.
	if len(env.Event) > 0 {
		if trimmed, cut := CutRawForLimit(env.Event, w.maxMediaBytes); cut {
			env.Event = trimmed
		}
	}
	payload, err := json.Marshal(env)
	if err != nil {
		w.log.ErrorContext(ctx, "webhook delivery: marshal envelope",
			"instance_id", j.instanceID, "event_id", j.eventID, "error", err)
		return
	}

	var lastErr error
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return
		}
		done, err := w.attempt(ctx, j, payload)
		if done {
			return
		}
		lastErr = err
		if attempt < MaxAttempts {
			if err := w.sleep(ctx, webhookBackoff(attempt)); err != nil {
				return
			}
		}
	}
	// Dead-letter: exactly ONE error log per exhausted job, then continue
	// with the next job. It carries instance, event and attempt metadata but
	// never the key.
	w.log.ErrorContext(ctx, "webhook dead letter",
		"instance_id", j.instanceID, "event_id", j.eventID, "event_type", j.eventType,
		"attempts", MaxAttempts, "last_error", lastErr)
}

// attempt runs one delivery attempt. It reports done=true when the job is
// finished (delivered, silently skipped, or terminally dropped) and
// done=false with the failure when the retry schedule must continue.
func (w *Worker) attempt(ctx context.Context, j job, payload []byte) (bool, error) {
	instance, err := w.loader.Get(ctx, j.instanceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			w.log.WarnContext(ctx, "webhook drop: instance gone",
				"instance_id", j.instanceID, "event_id", j.eventID)
			return true, nil
		}
		return false, fmt.Errorf("load instance: %w", err)
	}
	if instance == nil {
		w.log.WarnContext(ctx, "webhook drop: instance gone",
			"instance_id", j.instanceID, "event_id", j.eventID)
		return true, nil
	}

	var url string
	if instance.WebhookURL != nil {
		url = *instance.WebhookURL
	}
	// Silent skip (no HTTP, no failure): disabled, URL-less, or unsubscribed
	// jobs never reach Deliver.
	if !ShouldDeliver(WebhookConfig{URL: url, Enabled: instance.WebhookEnabled, Events: instance.WebhookEvents}, j.eventType) {
		return true, nil
	}

	// Attempt-time key resolution: a rotation during backoff takes effect on
	// the very next attempt. A miss is a recorded failure on the normal
	// retry schedule — the rotation may land mid-backoff.
	key, ok := w.keys.Get(j.instanceID)
	if !ok {
		w.log.WarnContext(ctx, "webhook delivery failed: no instance key cached",
			"instance_id", j.instanceID, "event_id", j.eventID, "event_type", j.eventType)
		return false, errors.New("webhook deliver: no instance key cached")
	}

	if err := w.deliver(ctx, url, key, payload); err != nil {
		// Defensive floor under the gate: a deliver function reporting a
		// skip drops the job without retry or dead-letter.
		if errors.Is(err, ErrSkipped) {
			return true, nil
		}
		w.log.WarnContext(ctx, "webhook delivery failed",
			"instance_id", j.instanceID, "event_id", j.eventID, "event_type", j.eventType, "error", err)
		return false, err
	}
	return true, nil
}

// sleep waits out one backoff through the injected function, defaulting to a
// context-aware real sleep.
func (w *Worker) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

// webhookBackoff returns the sleep after failed attempt n (1-based): 1s
// doubling per attempt, capped at 5 minutes.
func webhookBackoff(attempt int) time.Duration {
	delay := webhookBackoffBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= webhookBackoffMax {
			return webhookBackoffMax
		}
	}
	if delay > webhookBackoffMax {
		return webhookBackoffMax
	}
	return delay
}

// sleepContext waits for d, returning the context error when cancelled first.
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
