package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// stubLoader is an in-memory InstanceLoader for the worker tests.
type stubLoader struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*model.Instance
	err       error
	calls     int
}

func newStubLoader(instances ...*model.Instance) *stubLoader {
	loader := &stubLoader{instances: make(map[uuid.UUID]*model.Instance)}
	for _, instance := range instances {
		loader.instances[instance.ID] = instance
	}
	return loader
}

func (l *stubLoader) Get(_ context.Context, id uuid.UUID) (*model.Instance, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	instance, ok := l.instances[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	stored := *instance
	return &stored, nil
}

func (l *stubLoader) getCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// webhookTestInstance builds an enabled instance subscribed to every event
// type, pointed at url.
func webhookTestInstance(url string) *model.Instance {
	return &model.Instance{
		ID:             uuid.New(),
		Name:           "loja",
		Status:         "connected",
		WebhookURL:     &url,
		WebhookEnabled: true,
		WebhookEvents:  DefaultEvents(),
	}
}

// stubWriter records the envelopes the fan-out delegates to the inner writer.
type stubWriter struct {
	mu        sync.Mutex
	subjects  []string
	envelopes []events.Envelope
	writeErr  error
}

func (w *stubWriter) Write(_ context.Context, subject string, env events.Envelope) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.subjects = append(w.subjects, subject)
	w.envelopes = append(w.envelopes, env)
	return w.writeErr
}

func (w *stubWriter) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.subjects)
}

// flakyReceptor is a scripted webhook endpoint: it answers the n-th request
// with statuses[min(n, len(statuses)-1)] and records every body and apikey
// header. An optional release channel holds each request open until it is
// closed (or the request context ends).
type flakyReceptor struct {
	t        *testing.T
	mu       sync.Mutex
	statuses []int
	bodies   []string
	headers  []string
	requests int

	release     chan struct{}
	started     chan struct{}
	startedOnce sync.Once
}

func newFlakyReceptor(t *testing.T, statuses ...int) *flakyReceptor {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []int{http.StatusOK}
	}
	return &flakyReceptor{t: t, statuses: statuses, started: make(chan struct{})}
}

func (r *flakyReceptor) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.startedOnce.Do(func() { close(r.started) })
		if r.release != nil {
			select {
			case <-r.release:
			case <-req.Context().Done():
				return
			}
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			r.t.Errorf("read receptor body: %v", err)
		}
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			r.t.Errorf("Content-Type = %q, want application/json", ct)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		n := len(r.bodies)
		if n >= len(r.statuses) {
			n = len(r.statuses) - 1
		}
		status := r.statuses[n]
		r.bodies = append(r.bodies, string(body))
		r.headers = append(r.headers, req.Header.Get("apikey"))
		r.requests++
		w.WriteHeader(status)
	}
}

func (r *flakyReceptor) hits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

func (r *flakyReceptor) snapshot() (bodies, headers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...), append([]string(nil), r.headers...)
}

// testEnvelope builds a message envelope for the worker tests.
func testEnvelope(t *testing.T, eventType string, instanceID uuid.UUID) events.Envelope {
	t.Helper()
	env, err := events.New(eventType, instanceID, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	return env
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %v: %s", timeout, msg)
	}
}

// instantWorker builds a worker whose backoff sleeps nothing, so the retry
// tests run instant.
func instantWorker(loader InstanceLoader, keys *KeyCache, deliver func(ctx context.Context, url, key string, payload []byte) error, log *slog.Logger) *Worker {
	worker := NewWorker(loader, keys, deliver, 16<<20, log)
	worker.Sleep = func(context.Context, time.Duration) error { return nil }
	return worker
}

// syncBuffer is a goroutine-safe log sink: the worker logs from its own
// goroutines while the test polls the output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(s))
}

func (b *syncBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Count(b.buf.Bytes(), []byte(s))
}

func (b *syncBuffer) str() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testBufferLogger(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// TestFanoutAlwaysCallsInner pins the fan-out contract: every Write reaches
// the inner NATS writer, even when the inner write fails — and the webhook
// job is still queued in that case.
func TestFanoutAlwaysCallsInner(t *testing.T) {
	inner := &stubWriter{}
	worker := instantWorker(newStubLoader(), NewKeyCache(), Deliver, slog.Default())
	fanout := worker.Fanout(inner)

	env := testEnvelope(t, "message", uuid.New())
	if err := fanout.Write(context.Background(), "wzap.instances.x.message", env); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := inner.calls(); got != 1 {
		t.Fatalf("inner calls = %d, want 1", got)
	}

	inner.writeErr = errors.New("nats down")
	env2 := testEnvelope(t, "receipt", uuid.New())
	if err := fanout.Write(context.Background(), "wzap.instances.x.receipt", env2); err == nil {
		t.Fatal("Write with failing inner: error = nil, want the NATS error back")
	}
	if got := inner.calls(); got != 2 {
		t.Fatalf("inner calls = %d, want 2 (webhook never breaks the NATS path)", got)
	}
	if got := len(worker.queue); got != 2 {
		t.Fatalf("queued jobs = %d, want 2 (NATS errors never break webhook)", got)
	}
}

// TestFanoutNonBlockingDropCounted fills the 1000 buffer and proves the next
// dispatch still returns immediately, dropping the event with a counter.
func TestFanoutNonBlockingDropCounted(t *testing.T) {
	var buf syncBuffer
	inner := &stubWriter{}
	worker := instantWorker(newStubLoader(), NewKeyCache(), Deliver, testBufferLogger(&buf))
	fanout := worker.Fanout(inner)

	env := testEnvelope(t, "message", uuid.New())
	for i := 0; i < BufferSize; i++ {
		if err := fanout.Write(context.Background(), "subject", env); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if got := len(worker.queue); got != BufferSize {
		t.Fatalf("queued jobs = %d, want a full %d buffer", got, BufferSize)
	}

	done := make(chan error, 1)
	go func() { done <- fanout.Write(context.Background(), "subject", env) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("overflow Write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("overflow dispatch blocked on a full buffer, want a non-blocking drop")
	}
	if got := worker.Dropped(); got != 1 {
		t.Errorf("dropped = %d, want 1", got)
	}
	if got := inner.calls(); got != BufferSize+1 {
		t.Errorf("inner calls = %d, want %d (a drop never breaks the NATS path)", got, BufferSize+1)
	}
	if !buf.contains("dropping") {
		t.Errorf("log lacks the drop warning: %s", buf.str())
	}
}

// TestBackoffSchedule pins the retry sleeps: 1s doubling per attempt, capped
// at 5 minutes.
func TestBackoffSchedule(t *testing.T) {
	for attempt, want := range map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 32 * time.Second,
		7: 64 * time.Second,
	} {
		if got := webhookBackoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
	if got := webhookBackoff(20); got != 5*time.Minute {
		t.Errorf("backoff(20) = %v, want the 5m cap", got)
	}
}

// TestWorkerRetryThenSuccess drives a fail-twice-then-2xx receptor: exactly 3
// hits carrying the same event id.
func TestWorkerRetryThenSuccess(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	instance := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(instance.ID, "test-key-retry-success")
	worker := instantWorker(newStubLoader(instance), keys, Deliver, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()

	env := testEnvelope(t, "message", instance.ID)
	if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", env); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool { return receptor.hits() == 3 }, "receptor to see 3 attempts")
	bodies, headers := receptor.snapshot()
	if len(bodies) != 3 {
		t.Fatalf("hits = %d, want exactly 3", len(bodies))
	}
	first := bodies[0]
	for i, body := range bodies[1:] {
		if body != first {
			t.Fatalf("body %d differs from the first attempt (retry must resend byte-identical payloads)", i+1)
		}
	}
	var decoded struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal([]byte(first), &decoded); err != nil {
		t.Fatalf("decode retried body: %v", err)
	}
	if decoded.EventID != env.EventID.String() {
		t.Errorf("retried event_id = %s, want %s", decoded.EventID, env.EventID)
	}
	for i, header := range headers {
		if header != "test-key-retry-success" {
			t.Errorf("attempt %d apikey header = %q, want the instance key", i+1, header)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerDeadLetterThenNext pins the always-fail path: 8 attempts, one
// dead-letter log, and the following job still delivered.
func TestWorkerDeadLetterThenNext(t *testing.T) {
	failing := newFlakyReceptor(t, http.StatusInternalServerError)
	failingSrv := httptest.NewServer(failing.handler())
	t.Cleanup(failingSrv.Close)
	next := newFlakyReceptor(t, http.StatusOK)
	nextSrv := httptest.NewServer(next.handler())
	t.Cleanup(nextSrv.Close)

	bad := webhookTestInstance(failingSrv.URL)
	good := webhookTestInstance(nextSrv.URL)
	keys := NewKeyCache()
	keys.Store(bad.ID, "test-key-dead-letter")
	keys.Store(good.ID, "test-key-next-job")
	var buf syncBuffer
	worker := instantWorker(newStubLoader(bad, good), keys, Deliver, testBufferLogger(&buf))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	badEnv := testEnvelope(t, "message", bad.ID)
	goodEnv := testEnvelope(t, "receipt", good.ID)
	fanout := worker.Fanout(&stubWriter{})
	if err := fanout.Write(ctx, "subject", badEnv); err != nil {
		t.Fatalf("dispatch bad: %v", err)
	}
	if err := fanout.Write(ctx, "subject", goodEnv); err != nil {
		t.Fatalf("dispatch good: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool { return failing.hits() == MaxAttempts }, "failing receptor to see 8 attempts")
	waitFor(t, 5*time.Second, func() bool { return next.hits() == 1 }, "next job to be delivered")
	if got := failing.hits(); got != MaxAttempts {
		t.Errorf("failing hits = %d, want exactly %d", got, MaxAttempts)
	}

	waitFor(t, 5*time.Second, func() bool { return buf.contains("dead letter") }, "dead-letter log")
	logged := buf.str()
	for _, want := range []string{bad.ID.String(), badEnv.EventID.String(), `"message"`, `"attempts":8`} {
		if !buf.contains(want) {
			t.Errorf("dead-letter log lacks %s:\n%s", want, logged)
		}
	}
	if buf.contains("test-key-dead-letter") {
		t.Errorf("dead-letter log exposes the instance key:\n%s", logged)
	}
	deadLetters := buf.count("dead letter")
	if deadLetters != 1 {
		t.Errorf("dead-letter logs = %d, want exactly 1", deadLetters)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerSkipPaths proves disabled, URL-less and unsubscribed jobs produce
// zero HTTP hits.
func TestWorkerSkipPaths(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	newInstance := func(mut func(*model.Instance)) *model.Instance {
		instance := webhookTestInstance(srv.URL)
		mut(instance)
		return instance
	}
	nilURL := newInstance(func(i *model.Instance) { i.WebhookURL = nil })
	emptyURL := ""
	empty := newInstance(func(i *model.Instance) { i.WebhookURL = &emptyURL })
	disabled := newInstance(func(i *model.Instance) { i.WebhookEnabled = false })
	unsubscribed := newInstance(func(i *model.Instance) { i.WebhookEvents = []string{"receipt"} })

	keys := NewKeyCache()
	for _, instance := range []*model.Instance{nilURL, empty, disabled, unsubscribed} {
		keys.Store(instance.ID, "test-key-skip")
	}
	worker := instantWorker(newStubLoader(nilURL, empty, disabled, unsubscribed), keys, Deliver, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	fanout := worker.Fanout(&stubWriter{})
	for _, instance := range []*model.Instance{nilURL, empty, disabled, unsubscribed} {
		if err := fanout.Write(ctx, "subject", testEnvelope(t, "message", instance.ID)); err != nil {
			t.Fatalf("dispatch %s: %v", instance.ID, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := receptor.hits(); got != 0 {
		t.Errorf("requests received = %d, want 0 (skipped jobs never POST)", got)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerMissingKeyRecordedWithoutExposure runs the full retry schedule
// with a cold key cache: zero HTTP hits, a dead-letter, and no key material
// anywhere in the logs (a warm decoy entry proves logs never carry keys).
func TestWorkerMissingKeyRecordedWithoutExposure(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	instance := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(uuid.New(), "decoy-secret-never-logged")
	var buf syncBuffer
	var sleeps atomic.Int32
	worker := NewWorker(newStubLoader(instance), keys, Deliver, 16<<20, testBufferLogger(&buf))
	worker.Sleep = func(context.Context, time.Duration) error { sleeps.Add(1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	env := testEnvelope(t, "message", instance.ID)
	if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", env); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool { return sleeps.Load() == MaxAttempts-1 }, "missing-key job to exhaust its retries")
	waitFor(t, 5*time.Second, func() bool { return buf.contains("dead letter") }, "dead-letter log")
	if got := receptor.hits(); got != 0 {
		t.Errorf("requests received = %d, want 0 (nothing is sent without a key)", got)
	}
	if buf.contains("decoy-secret-never-logged") {
		t.Errorf("log exposes key material:\n%s", buf.str())
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerMissingInstanceDropsWithoutRetry proves a job whose instance is
// gone is dropped with a log and no retry: the target is gone.
func TestWorkerMissingInstanceDropsWithoutRetry(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	gone := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(gone.ID, "test-key-missing-instance")
	loader := newStubLoader() // knows no instance: every Get is ErrNotFound.
	var buf syncBuffer
	var sleeps atomic.Int32
	worker := NewWorker(loader, keys, Deliver, 16<<20, testBufferLogger(&buf))
	worker.Sleep = func(context.Context, time.Duration) error { sleeps.Add(1); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", testEnvelope(t, "message", gone.ID)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return loader.getCalls() == 1 }, "worker to load the instance once")
	time.Sleep(200 * time.Millisecond)
	if got := receptor.hits(); got != 0 {
		t.Errorf("requests received = %d, want 0 (a gone instance never POSTs)", got)
	}
	if got := sleeps.Load(); got != 0 {
		t.Errorf("backoff sleeps = %d, want 0 (no retry when the target is gone)", got)
	}
	if got := loader.getCalls(); got != 1 {
		t.Errorf("loader calls = %d, want exactly 1", got)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerRotationMidBackoff swaps the cached key during the first backoff
// sleep: the following attempts must carry the new key.
func TestWorkerRotationMidBackoff(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusInternalServerError, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	instance := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(instance.ID, "test-key-rotation-old")
	worker := NewWorker(newStubLoader(instance), keys, Deliver, 16<<20, slog.Default())
	worker.Sleep = func(context.Context, time.Duration) error {
		keys.Store(instance.ID, "test-key-rotation-new")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", testEnvelope(t, "message", instance.ID)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return receptor.hits() == 2 }, "rotated delivery to succeed")
	_, headers := receptor.snapshot()
	if len(headers) != 2 || headers[0] != "test-key-rotation-old" || headers[1] != "test-key-rotation-new" {
		t.Errorf("apikey headers = %q, want [old new] (attempt-time key resolution)", headers)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
}

// TestWorkerShutdownAbortsInflight holds a delivery open, cancels the run
// context, and requires the worker to stop without another attempt.
func TestWorkerShutdownAbortsInflight(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusOK)
	receptor.release = make(chan struct{})
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(receptor.release) })

	instance := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(instance.ID, "test-key-shutdown")
	var buf syncBuffer
	worker := NewWorker(newStubLoader(instance), keys, Deliver, 16<<20, testBufferLogger(&buf))
	var sleeps atomic.Int32
	worker.Sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sleeps.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()

	if err := worker.Fanout(&stubWriter{}).Write(ctx, "subject", testEnvelope(t, "message", instance.ID)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-receptor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight delivery never started")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not abort the in-flight delivery on cancel")
	}
	if got := sleeps.Load(); got != 0 {
		t.Errorf("backoff sleeps after cancel = %d, want 0 (no retry past shutdown)", got)
	}
}

// TestWorkerShutdownDropsPending proves the no-drain rule: a cancelled run
// context drops the queued jobs with a count log instead of delivering them.
func TestWorkerShutdownDropsPending(t *testing.T) {
	receptor := newFlakyReceptor(t, http.StatusOK)
	srv := httptest.NewServer(receptor.handler())
	t.Cleanup(srv.Close)

	first := webhookTestInstance(srv.URL)
	second := webhookTestInstance(srv.URL)
	keys := NewKeyCache()
	keys.Store(first.ID, "test-key-pending-1")
	keys.Store(second.ID, "test-key-pending-2")
	var buf syncBuffer
	worker := instantWorker(newStubLoader(first, second), keys, Deliver, testBufferLogger(&buf))

	fanout := worker.Fanout(&stubWriter{})
	ctx := context.Background()
	if err := fanout.Write(ctx, "subject", testEnvelope(t, "message", first.ID)); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	if err := fanout.Write(ctx, "subject", testEnvelope(t, "message", second.ID)); err != nil {
		t.Fatalf("dispatch second: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	worker.Run(cancelled)

	if got := receptor.hits(); got != 0 {
		t.Errorf("requests received = %d, want 0 (pending queue is dropped, not drained)", got)
	}
	if !buf.contains("pending") {
		t.Errorf("stop log lacks the dropped pending count:\n%s", buf.str())
	}
}
