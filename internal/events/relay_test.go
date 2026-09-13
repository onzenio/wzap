package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustEnvelope(t *testing.T, instanceID uuid.UUID, eventType string) Envelope {
	t.Helper()

	env, err := New(eventType, instanceID, map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return env
}

func outboxRow(t *testing.T, env Envelope, subject string) model.OutboxEvent {
	t.Helper()

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return model.OutboxEvent{ID: env.EventID, Subject: subject, Envelope: data}
}

type enqueuedEvent struct {
	id       uuid.UUID
	subject  string
	envelope []byte
}

// fakeOutbox is an in-memory storage.EventOutboxRepository for relay tests.
type fakeOutbox struct {
	events     []model.OutboxEvent
	claims     int
	claimErr   error
	published  map[uuid.UUID]int
	attempts   map[uuid.UUID]int
	lastErrors map[uuid.UUID]string
	deleted    []time.Time
	deleteErr  error
	enqueued   []enqueuedEvent
}

func newFakeOutbox(events ...model.OutboxEvent) *fakeOutbox {
	return &fakeOutbox{
		events:     events,
		published:  map[uuid.UUID]int{},
		attempts:   map[uuid.UUID]int{},
		lastErrors: map[uuid.UUID]string{},
	}
}

func (f *fakeOutbox) Enqueue(_ context.Context, id uuid.UUID, subject string, envelope []byte) error {
	f.enqueued = append(f.enqueued, enqueuedEvent{id: id, subject: subject, envelope: envelope})
	return nil
}

func (f *fakeOutbox) ClaimPending(_ context.Context, limit int) ([]model.OutboxEvent, error) {
	f.claims++
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if limit <= 0 {
		return []model.OutboxEvent{}, nil
	}

	pending := []model.OutboxEvent{}
	for _, event := range f.events {
		if event.PublishedAt == nil {
			pending = append(pending, event)
			if len(pending) == limit {
				break
			}
		}
	}
	return pending, nil
}

func (f *fakeOutbox) MarkPublished(_ context.Context, id uuid.UUID) error {
	f.published[id]++
	now := time.Now()
	for i := range f.events {
		if f.events[i].ID == id {
			f.events[i].PublishedAt = &now
		}
	}
	return nil
}

func (f *fakeOutbox) MarkAttempt(_ context.Context, id uuid.UUID, errMsg string) error {
	f.attempts[id]++
	f.lastErrors[id] = errMsg
	for i := range f.events {
		if f.events[i].ID == id {
			f.events[i].Attempts++
			f.events[i].LastError = errMsg
		}
	}
	return nil
}

func (f *fakeOutbox) DeletePublishedBefore(_ context.Context, t time.Time) (int64, error) {
	f.deleted = append(f.deleted, t)
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}

	var removed int64
	kept := make([]model.OutboxEvent, 0, len(f.events))
	for _, event := range f.events {
		if event.PublishedAt != nil && event.PublishedAt.Before(t) {
			removed++
			continue
		}
		kept = append(kept, event)
	}
	f.events = kept
	return removed, nil
}

type publishedEvent struct {
	subject string
	env     Envelope
}

// fakePublisher records every call and can fail ensures or publishes.
type fakePublisher struct {
	ensureCalls int
	ensureErr   error
	publishErr  error
	failFirst   int
	failErr     error
	calls       []publishedEvent
	onPublish   func()
}

func (f *fakePublisher) EnsureStream(context.Context) error {
	f.ensureCalls++
	return f.ensureErr
}

func (f *fakePublisher) Publish(_ context.Context, subject string, env Envelope) error {
	f.calls = append(f.calls, publishedEvent{subject: subject, env: env})
	if f.onPublish != nil {
		f.onPublish()
	}
	if f.failFirst > 0 {
		f.failFirst--
		return f.failErr
	}
	return f.publishErr
}

func TestOutboxWriterEnqueuesEnvelope(t *testing.T) {
	outbox := newFakeOutbox()
	writer := NewWriter(outbox)
	env := mustEnvelope(t, uuid.New(), "message")
	subject := Subjects.Message(env.InstanceID)

	if err := writer.Write(context.Background(), subject, env); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(outbox.enqueued) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(outbox.enqueued))
	}

	got := outbox.enqueued[0]
	if got.id != env.EventID {
		t.Errorf("enqueued id = %s, want %s", got.id, env.EventID)
	}
	if got.subject != subject {
		t.Errorf("enqueued subject = %q, want %q", got.subject, subject)
	}

	var decoded Envelope
	if err := json.Unmarshal(got.envelope, &decoded); err != nil {
		t.Fatalf("decode enqueued envelope: %v", err)
	}
	if decoded.EventID != env.EventID {
		t.Errorf("enqueued event_id = %s, want %s", decoded.EventID, env.EventID)
	}
	if decoded.Type != env.Type || decoded.InstanceID != env.InstanceID {
		t.Errorf("enqueued envelope = %+v, want type %q instance %s", decoded, env.Type, env.InstanceID)
	}
	if !decoded.OccurredAt.Equal(env.OccurredAt) {
		t.Errorf("enqueued occurred_at = %v, want %v", decoded.OccurredAt, env.OccurredAt)
	}
	if string(decoded.Payload) != string(env.Payload) {
		t.Errorf("enqueued payload = %s, want %s", decoded.Payload, env.Payload)
	}
}

func TestOutboxWriterRejectsInvalidEnvelope(t *testing.T) {
	outbox := newFakeOutbox()
	writer := NewWriter(outbox)
	env := Envelope{EventID: uuid.New(), Payload: json.RawMessage(`{"broken":`)}

	if err := writer.Write(context.Background(), "wzap.instances.x.message", env); err == nil {
		t.Fatal("Write succeeded with an invalid payload")
	}
	if len(outbox.enqueued) != 0 {
		t.Errorf("Enqueue called %d times for an invalid envelope, want 0", len(outbox.enqueued))
	}
}

func TestRelayPublishNowMarksEventsPublished(t *testing.T) {
	ctx := context.Background()
	instanceID := uuid.New()
	messageEnv := mustEnvelope(t, instanceID, "message")
	connectionEnv := mustEnvelope(t, instanceID, "connection")
	first := outboxRow(t, messageEnv, Subjects.Message(instanceID))
	second := outboxRow(t, connectionEnv, Subjects.Connection(instanceID))

	outbox := newFakeOutbox(first, second)
	publisher := &fakePublisher{}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)

	if err := relay.PublishNow(ctx, []model.OutboxEvent{first, second}); err != nil {
		t.Fatalf("PublishNow: %v", err)
	}

	if len(publisher.calls) != 2 {
		t.Fatalf("Publish called %d times, want 2", len(publisher.calls))
	}
	if publisher.calls[0].subject != Subjects.Message(instanceID) {
		t.Errorf("first publish subject = %q, want %q", publisher.calls[0].subject, Subjects.Message(instanceID))
	}
	if publisher.calls[0].env.EventID != messageEnv.EventID {
		t.Errorf("first published event = %s, want %s", publisher.calls[0].env.EventID, messageEnv.EventID)
	}
	if publisher.calls[1].subject != Subjects.Connection(instanceID) {
		t.Errorf("second publish subject = %q, want %q", publisher.calls[1].subject, Subjects.Connection(instanceID))
	}
	if outbox.published[first.ID] != 1 || outbox.published[second.ID] != 1 {
		t.Errorf("published events = %v, want both %s and %s", outbox.published, first.ID, second.ID)
	}
	if len(outbox.attempts) != 0 {
		t.Errorf("attempts recorded for a successful publish: %v", outbox.attempts)
	}

	pending, err := outbox.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("ClaimPending returned %d events after publish, want 0", len(pending))
	}
}

func TestRelayPublishNowFailureKeepsEventPending(t *testing.T) {
	ctx := context.Background()
	instanceID := uuid.New()
	env := mustEnvelope(t, instanceID, "message")
	row := outboxRow(t, env, Subjects.Message(instanceID))

	outbox := newFakeOutbox(row)
	publishErr := errors.New("broker unavailable")
	publisher := &fakePublisher{publishErr: publishErr}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)

	err := relay.PublishNow(ctx, []model.OutboxEvent{row})
	if err == nil {
		t.Fatal("PublishNow succeeded with a failing publisher")
	}
	if !errors.Is(err, publishErr) {
		t.Errorf("PublishNow error = %v, want it to wrap %v", err, publishErr)
	}
	if outbox.published[row.ID] != 0 {
		t.Errorf("event was marked published after a failed publish")
	}
	if outbox.attempts[row.ID] != 1 {
		t.Errorf("attempts = %d, want 1", outbox.attempts[row.ID])
	}
	if !strings.Contains(outbox.lastErrors[row.ID], "broker unavailable") {
		t.Errorf("last error = %q, want it to record the publish failure", outbox.lastErrors[row.ID])
	}

	pending, err := outbox.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != row.ID {
		t.Errorf("pending after failure = %+v, want the failed event %s", pending, row.ID)
	}
}

func TestRelayPublishNowContinuesAfterFailure(t *testing.T) {
	ctx := context.Background()
	instanceID := uuid.New()
	failed := outboxRow(t, mustEnvelope(t, instanceID, "message"), Subjects.Message(instanceID))
	published := outboxRow(t, mustEnvelope(t, instanceID, "receipt"), Subjects.Receipt(instanceID))

	outbox := newFakeOutbox(failed, published)
	publisher := &fakePublisher{failFirst: 1, failErr: errors.New("broker unavailable")}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)

	err := relay.PublishNow(ctx, []model.OutboxEvent{failed, published})
	if err == nil {
		t.Fatal("PublishNow succeeded with a failing publisher")
	}
	if len(publisher.calls) != 2 {
		t.Fatalf("Publish called %d times, want 2 (one failure must not stop the batch)", len(publisher.calls))
	}
	if outbox.attempts[failed.ID] != 1 {
		t.Errorf("failed event attempts = %d, want 1", outbox.attempts[failed.ID])
	}
	if outbox.published[failed.ID] != 0 {
		t.Errorf("failed event was marked published")
	}
	if outbox.published[published.ID] != 1 {
		t.Errorf("second event published count = %d, want 1", outbox.published[published.ID])
	}
}

func TestRelayPublishNowMalformedEnvelopeRecordsAttempt(t *testing.T) {
	ctx := context.Background()
	row := model.OutboxEvent{
		ID:       uuid.New(),
		Subject:  "wzap.instances.x.message",
		Envelope: []byte(`{"event_id":`),
	}

	outbox := newFakeOutbox(row)
	publisher := &fakePublisher{}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)

	if err := relay.PublishNow(ctx, []model.OutboxEvent{row}); err == nil {
		t.Fatal("PublishNow succeeded with a malformed envelope")
	}
	if len(publisher.calls) != 0 {
		t.Errorf("Publish called %d times for a malformed envelope, want 0", len(publisher.calls))
	}
	if outbox.attempts[row.ID] != 1 {
		t.Errorf("attempts = %d, want 1", outbox.attempts[row.ID])
	}
}

func TestRelayBackoff(t *testing.T) {
	relay := NewRelay(newFakeOutbox(), &fakePublisher{}, discardLogger(), 7)
	relay.backoffBase = time.Second
	relay.backoffMax = 8 * time.Second

	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 0},
		{failures: 1, want: time.Second},
		{failures: 2, want: 2 * time.Second},
		{failures: 3, want: 4 * time.Second},
		{failures: 4, want: 8 * time.Second},
		{failures: 5, want: 8 * time.Second},
		{failures: 50, want: 8 * time.Second},
	}

	for _, tt := range tests {
		if got := relay.backoff(tt.failures); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.failures, got, tt.want)
		}
	}
}

func TestRelayCleanupUsesRetentionWindow(t *testing.T) {
	fixed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	outbox := newFakeOutbox()
	relay := NewRelay(outbox, &fakePublisher{}, discardLogger(), 7)
	relay.now = func() time.Time { return fixed }

	relay.cleanup(context.Background())

	if len(outbox.deleted) != 1 {
		t.Fatalf("DeletePublishedBefore called %d times, want 1", len(outbox.deleted))
	}
	if want := fixed.AddDate(0, 0, -7); !outbox.deleted[0].Equal(want) {
		t.Errorf("cleanup cutoff = %v, want %v", outbox.deleted[0], want)
	}
}

func TestRelayRunRetriesFailedEventsWithBackoff(t *testing.T) {
	instanceID := uuid.New()
	env := mustEnvelope(t, instanceID, "message")
	row := outboxRow(t, env, Subjects.Message(instanceID))

	outbox := newFakeOutbox(row)
	publisher := &fakePublisher{failFirst: 1, failErr: errors.New("broker unavailable")}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)
	relay.batchSize = 1
	relay.pollInterval = time.Hour
	relay.backoffBase = 2 * time.Second
	relay.backoffMax = time.Minute
	relay.cleanupInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher.onPublish = func() {
		if len(publisher.calls) == 2 {
			cancel()
		}
	}

	var delays []time.Duration
	relay.sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}

	relay.Run(ctx)

	if len(publisher.calls) != 2 {
		t.Fatalf("Publish called %d times, want 2 (one failure, one retry)", len(publisher.calls))
	}
	if outbox.attempts[env.EventID] != 1 {
		t.Errorf("attempts = %d, want 1", outbox.attempts[env.EventID])
	}
	if outbox.published[env.EventID] != 1 {
		t.Errorf("published = %d, want 1 after the retry", outbox.published[env.EventID])
	}
	if len(delays) != 1 || delays[0] != relay.backoffBase {
		t.Errorf("delays = %v, want a single backoff of %v before the retry", delays, relay.backoffBase)
	}
	if publisher.ensureCalls != 2 {
		t.Errorf("EnsureStream calls = %d, want 2 (boot and after the failure)", publisher.ensureCalls)
	}
	if len(outbox.deleted) != 1 {
		t.Errorf("cleanup ran %d times, want 1", len(outbox.deleted))
	}
}

func TestRelayRunWaitsForStreamBeforeClaiming(t *testing.T) {
	instanceID := uuid.New()
	row := outboxRow(t, mustEnvelope(t, instanceID, "message"), Subjects.Message(instanceID))

	outbox := newFakeOutbox(row)
	publisher := &fakePublisher{ensureErr: errors.New("broker unavailable")}
	relay := NewRelay(outbox, publisher, discardLogger(), 7)
	relay.batchSize = 10
	relay.pollInterval = time.Hour
	relay.backoffBase = 3 * time.Second
	relay.backoffMax = time.Minute
	relay.cleanupInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var delays []time.Duration
	relay.sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		cancel()
		return context.Canceled
	}

	relay.Run(ctx)

	if outbox.claims != 0 {
		t.Errorf("claimed %d times while the stream was not ensured, want 0", outbox.claims)
	}
	if len(publisher.calls) != 0 {
		t.Errorf("published %d events while the stream was not ensured, want 0", len(publisher.calls))
	}
	if len(delays) != 1 || delays[0] != relay.backoffBase {
		t.Errorf("delays = %v, want a single backoff of %v", delays, relay.backoffBase)
	}
}
