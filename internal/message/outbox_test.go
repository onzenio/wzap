package message

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/instancelock"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
)

// retryRecord records one MarkRetrying call.
type retryRecord struct {
	id            uuid.UUID
	errMsg        string
	nextAttemptAt time.Time
}

// fakeOutboxRepo is an in-memory OutboxStore. ClaimQueued hands out the queued
// messages oldest first and records every state transition.
type fakeOutboxRepo struct {
	mu sync.Mutex

	queue []model.OutboundMessage

	claimLimits  []int
	claimErr     error
	sent         map[uuid.UUID]string
	failed       map[uuid.UUID]string
	retries      []retryRecord
	requeues     []time.Time
	requeueCount int64
	requeueErr   error

	markSentErr   error
	markFailedErr error
	markRetryErr  error
}

func newFakeOutboxRepo(messages ...model.OutboundMessage) *fakeOutboxRepo {
	return &fakeOutboxRepo{
		queue:  messages,
		sent:   make(map[uuid.UUID]string),
		failed: make(map[uuid.UUID]string),
	}
}

func (f *fakeOutboxRepo) ClaimQueued(_ context.Context, limit int) ([]model.OutboundMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimLimits = append(f.claimLimits, limit)
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if limit <= 0 {
		return []model.OutboundMessage{}, nil
	}
	if len(f.queue) < limit {
		limit = len(f.queue)
	}
	claimed := append([]model.OutboundMessage(nil), f.queue[:limit]...)
	f.queue = f.queue[limit:]
	for i := range claimed {
		claimed[i].Status = StatusSending
	}
	return claimed, nil
}

func (f *fakeOutboxRepo) MarkSent(_ context.Context, id uuid.UUID, whatsAppMessageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markSentErr != nil {
		return f.markSentErr
	}
	f.sent[id] = whatsAppMessageID
	return nil
}

func (f *fakeOutboxRepo) MarkFailed(_ context.Context, id uuid.UUID, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markFailedErr != nil {
		return f.markFailedErr
	}
	f.failed[id] = errMsg
	return nil
}

func (f *fakeOutboxRepo) MarkRetrying(_ context.Context, id uuid.UUID, errMsg string, nextAttemptAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markRetryErr != nil {
		return f.markRetryErr
	}
	f.retries = append(f.retries, retryRecord{id: id, errMsg: errMsg, nextAttemptAt: nextAttemptAt})
	return nil
}

func (f *fakeOutboxRepo) RequeueStuck(_ context.Context, olderThan time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requeueErr != nil {
		return 0, f.requeueErr
	}
	f.requeues = append(f.requeues, olderThan)
	return f.requeueCount, nil
}

// snapshot accessors keep the tests race-free.

func (f *fakeOutboxRepo) sentIDs() map[uuid.UUID]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]string, len(f.sent))
	for id, wamid := range f.sent {
		out[id] = wamid
	}
	return out
}

func (f *fakeOutboxRepo) failedMessages() map[uuid.UUID]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]string, len(f.failed))
	for id, errMsg := range f.failed {
		out[id] = errMsg
	}
	return out
}

func (f *fakeOutboxRepo) retryCalls() []retryRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]retryRecord(nil), f.retries...)
}

func (f *fakeOutboxRepo) requeueCalls() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.requeues...)
}

func (f *fakeOutboxRepo) claimCallLimits() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.claimLimits...)
}

// writtenEvent is one event handed to the fake writer.
type writtenEvent struct {
	subject  string
	envelope events.Envelope
}

// fakeWriter records the events enqueued in the outbox.
type fakeWriter struct {
	mu       sync.Mutex
	events   []writtenEvent
	writeErr error
}

func (f *fakeWriter) Write(_ context.Context, subject string, env events.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.events = append(f.events, writtenEvent{subject: subject, envelope: env})
	return nil
}

func (f *fakeWriter) written() []writtenEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writtenEvent(nil), f.events...)
}

// outboxFixture wires the outbox under test over the fakes.
type outboxFixture struct {
	outbox   *Outbox
	repo     *fakeOutboxRepo
	manager  *sessiontest.Fake
	writer   *fakeWriter
	session  *sessiontest.FakeSession
	media    *fakeMediaPaths
	instance uuid.UUID
	now      time.Time
}

func newOutboxFixture(messages ...model.OutboundMessage) *outboxFixture {
	instanceID := uuid.New()
	for i := range messages {
		if messages[i].InstanceID == uuid.Nil {
			messages[i].InstanceID = instanceID
		}
		if messages[i].ID == uuid.Nil {
			messages[i].ID = uuid.New()
		}
	}

	sess := sessiontest.NewSession(instanceID, nil)
	manager := sessiontest.New(nil)
	manager.Put(instanceID, sess)

	fixture := &outboxFixture{
		repo:     newFakeOutboxRepo(messages...),
		manager:  manager,
		writer:   &fakeWriter{},
		session:  sess,
		media:    &fakeMediaPaths{},
		instance: instanceID,
		now:      time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	}
	// One worker keeps the claim/process order deterministic; the worker pool
	// concurrency itself is covered by the locker tests. Humanization stays
	// off unless a test enables it explicitly.
	fixture.outbox = NewOutbox(fixture.repo, manager, fixture.writer, fixture.media, discardLogger(), 1, instancelock.New(), false)
	fixture.outbox.now = func() time.Time { return fixture.now }
	return fixture
}

// runOutbox drives one full claim/process pass: the injected sleep cancels the
// context on its first call, so Run returns after the workers idle.
func runOutbox(t *testing.T, fixture *outboxFixture) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	fixture.outbox.sleep = func(ctx context.Context, _ time.Duration) error {
		once.Do(cancel)
		return ctx.Err()
	}
	fixture.outbox.Run(ctx)
}

// textMessage builds a queued text message of the fixture instance.
func textMessage(instanceID uuid.UUID, attempts int) model.OutboundMessage {
	return model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		Type:         TypeText,
		RecipientJID: "5547988359190@s.whatsapp.net",
		Payload:      []byte(`{"text":"olá"}`),
		Status:       StatusQueued,
		Attempts:     attempts,
	}
}

// envelopePayload decodes the payload of a recorded event.
func envelopePayload(t *testing.T, env events.Envelope) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode event payload %q: %v", env.Payload, err)
	}
	return payload
}

func TestOutboxProcessesBatchAndMarksSent(t *testing.T) {
	first := textMessage(uuid.Nil, 0)
	second := textMessage(uuid.Nil, 0)
	fixture := newOutboxFixture(first, second)

	runOutbox(t, fixture)

	sent := fixture.repo.sentIDs()
	if len(sent) != 2 {
		t.Fatalf("sent = %d messages, want 2", len(sent))
	}
	if sent[first.ID] != "fake-wamid-1" || sent[second.ID] != "fake-wamid-2" {
		t.Errorf("sent = %v, want the session ids in order", sent)
	}

	calls := fixture.session.SendCalls()
	if len(calls) != 2 {
		t.Fatalf("session sends = %d, want 2", len(calls))
	}
	want := session.OutboundMessage{
		Type:         TypeText,
		RecipientJID: "5547988359190@s.whatsapp.net",
		Payload:      []byte(`{"text":"olá"}`),
	}
	for i, call := range calls {
		if call.Type != want.Type || call.RecipientJID != want.RecipientJID || string(call.Payload) != string(want.Payload) {
			t.Errorf("session send[%d] = %+v, want %+v", i, call, want)
		}
	}

	written := fixture.writer.written()
	if len(written) != 2 {
		t.Fatalf("events written = %d, want 2", len(written))
	}
	for i, event := range written {
		wantSubject := events.Subjects.MessageStatus(fixture.instance)
		if event.subject != wantSubject {
			t.Errorf("event[%d] subject = %q, want %q", i, event.subject, wantSubject)
		}
		if event.envelope.Type != messageStatusEventType {
			t.Errorf("event[%d] type = %q, want %q", i, event.envelope.Type, messageStatusEventType)
		}
		if event.envelope.InstanceID != fixture.instance {
			t.Errorf("event[%d] instance = %s, want %s", i, event.envelope.InstanceID, fixture.instance)
		}
		payload := envelopePayload(t, event.envelope)
		if payload["status"] != StatusSent {
			t.Errorf("event[%d] status = %v, want %q", i, payload["status"], StatusSent)
		}
		if payload["whatsapp_id"] != fmt.Sprintf("fake-wamid-%d", i+1) {
			t.Errorf("event[%d] whatsapp_id = %v, want fake-wamid-%d", i, payload["whatsapp_id"], i+1)
		}
	}
}

// waitFor polls condition until it holds or the deadline expires.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before the deadline")
}

func TestOutboxSerializesSameInstanceSends(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0), textMessage(uuid.Nil, 0))
	fixture.outbox.workers = 2

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, err := fixture.outbox.lock.Acquire(ctx, fixture.instance)
	if err != nil {
		t.Fatalf("acquire instance lock: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.outbox.Run(ctx)
	}()

	waitFor(t, func() bool { return len(fixture.repo.claimCallLimits()) > 0 })
	if len(fixture.session.SendCalls()) != 0 {
		t.Fatal("a send started while the instance lock was held")
	}

	release()

	waitFor(t, func() bool { return len(fixture.repo.sentIDs()) == 2 })
	cancel()
	<-done
}

func TestOutboxTransientFailureSchedulesRetry(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{name: "first failure", attempts: 0, want: time.Second},
		{name: "second failure", attempts: 1, want: 2 * time.Second},
		{name: "third failure", attempts: 2, want: 4 * time.Second},
		{name: "fourth failure", attempts: 3, want: 8 * time.Second},
		{name: "fifth failure", attempts: 4, want: 16 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newOutboxFixture(textMessage(uuid.Nil, tt.attempts))
			messageID := fixture.repo.queue[0].ID
			fixture.session.SendErr = fmt.Errorf("%w: network down", session.ErrTransient)

			runOutbox(t, fixture)

			retries := fixture.repo.retryCalls()
			if len(retries) != 1 {
				t.Fatalf("MarkRetrying calls = %d, want 1", len(retries))
			}
			retry := retries[0]
			if retry.id != messageID {
				t.Errorf("retried id = %s, want %s", retry.id, messageID)
			}
			if !retry.nextAttemptAt.Equal(fixture.now.Add(tt.want)) {
				t.Errorf("next_attempt_at = %s, want %s", retry.nextAttemptAt, fixture.now.Add(tt.want))
			}
			if !strings.Contains(retry.errMsg, "network down") {
				t.Errorf("retry error = %q, want the send failure", retry.errMsg)
			}
			if len(fixture.repo.failedMessages()) != 0 {
				t.Error("message was failed on a retryable error")
			}
			if len(fixture.writer.written()) != 0 {
				t.Error("a status event was written for a retry")
			}
		})
	}
}

func TestOutboxFailsMessageAfterMaxRetries(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, maxSendRetries))
	messageID := fixture.repo.queue[0].ID
	fixture.session.SendErr = fmt.Errorf("%w: network down", session.ErrTransient)

	runOutbox(t, fixture)

	if len(fixture.repo.retryCalls()) != 0 {
		t.Fatal("message was retried past the retry limit")
	}
	failed := fixture.repo.failedMessages()
	if len(failed) != 1 || !strings.Contains(failed[messageID], "network down") {
		t.Fatalf("failed = %v, want the message failed with the send error", failed)
	}

	written := fixture.writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	payload := envelopePayload(t, written[0].envelope)
	if payload["status"] != StatusFailed {
		t.Errorf("event status = %v, want %q", payload["status"], StatusFailed)
	}
	if payload["message_id"] != messageID.String() {
		t.Errorf("event message_id = %v, want %s", payload["message_id"], messageID)
	}
}

func TestOutboxDefinitiveFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "not connected", err: fmt.Errorf("%w: send text", session.ErrNotConnected)},
		{name: "invalid recipient", err: fmt.Errorf("%w: 123", session.ErrInvalidRecipient)},
		{name: "unsupported type", err: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
			messageID := fixture.repo.queue[0].ID
			if tt.err != nil {
				fixture.session.SendErr = tt.err
			} else {
				fixture.repo.queue[0].Type = "sticker"
			}

			runOutbox(t, fixture)

			if len(fixture.repo.retryCalls()) != 0 {
				t.Error("definitive failure was retried")
			}
			if _, ok := fixture.repo.sentIDs()[messageID]; ok {
				t.Error("definitive failure was marked sent")
			}
			if len(fixture.repo.failedMessages()) != 1 {
				t.Fatalf("failed = %d messages, want 1", len(fixture.repo.failedMessages()))
			}
			if len(fixture.writer.written()) != 1 {
				t.Fatalf("events written = %d, want 1", len(fixture.writer.written()))
			}
		})
	}
}

func TestOutboxMissingSessionFailsDefinitively(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
	messageID := fixture.repo.queue[0].ID
	fixture.manager = sessiontest.New(nil)
	fixture.outbox.manager = fixture.manager

	runOutbox(t, fixture)

	failed := fixture.repo.failedMessages()
	if len(failed) != 1 || !strings.Contains(failed[messageID], "session not found") {
		t.Fatalf("failed = %v, want the missing session error", failed)
	}
}

func TestOutboxInvalidPayloadFailsDefinitively(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
	messageID := fixture.repo.queue[0].ID
	fixture.repo.queue[0].Payload = []byte(`{"text":""}`)

	runOutbox(t, fixture)

	failed := fixture.repo.failedMessages()
	if len(failed) != 1 || !strings.Contains(failed[messageID], "text") {
		t.Fatalf("failed = %v, want the payload error", failed)
	}
	if len(fixture.session.SendCalls()) != 0 {
		t.Error("session was called with an empty text payload")
	}
}

func TestOutboxHumanizeSimulatesPresenceBeforeSend(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
	fixture.repo.queue[0].Payload = []byte(`{"text":"` + strings.Repeat("a", 100) + `"}`)

	var sleeps []time.Duration
	fixture.outbox.humanizer = Humanizer{
		Enabled: true,
		Sleep: func(_ context.Context, d time.Duration) error {
			if len(fixture.session.SendCalls()) != 0 {
				t.Error("the message was sent before the presence simulation finished")
			}
			sleeps = append(sleeps, d)
			return nil
		},
	}

	runOutbox(t, fixture)

	if len(sleeps) != 1 || sleeps[0] != 4*time.Second {
		t.Errorf("humanizer sleeps = %v, want a single 4s wait for the 100-character text", sleeps)
	}
	presence := fixture.session.PresenceCalls()
	if len(presence) != 2 {
		t.Fatalf("presence calls = %d, want composing and paused", len(presence))
	}
	for i, want := range []string{"composing", "paused"} {
		if presence[i].State != want || presence[i].ChatJID != "5547988359190@s.whatsapp.net" {
			t.Errorf("presence[%d] = %+v, want %q on the target chat", i, presence[i], want)
		}
	}
	if len(fixture.repo.sentIDs()) != 1 {
		t.Errorf("sent = %d messages, want the humanized message delivered", len(fixture.repo.sentIDs()))
	}
}

func TestOutboxHumanizeDisabledSkipsPresence(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
	slept := false
	fixture.outbox.humanizer = Humanizer{Sleep: func(context.Context, time.Duration) error {
		slept = true
		return nil
	}}

	runOutbox(t, fixture)

	if len(fixture.session.PresenceCalls()) != 0 {
		t.Errorf("presence calls = %v, want none when humanization is disabled", fixture.session.PresenceCalls())
	}
	if slept {
		t.Error("the humanizer slept while disabled")
	}
	if len(fixture.repo.sentIDs()) != 1 {
		t.Errorf("sent = %d messages, want the message delivered", len(fixture.repo.sentIDs()))
	}
}

func TestOutboxHumanizePresenceFailureIsBestEffort(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))
	fixture.outbox.humanizer = Humanizer{Enabled: true, Sleep: func(context.Context, time.Duration) error { return nil }}
	fixture.session.SendPresenceErr = errors.New("websocket write failed")

	var logs bytes.Buffer
	fixture.outbox.log = slog.New(slog.NewTextHandler(&logs, nil))

	runOutbox(t, fixture)

	if len(fixture.session.SendCalls()) != 1 {
		t.Fatalf("session sends = %d, want the message sent despite the presence failure", len(fixture.session.SendCalls()))
	}
	if len(fixture.repo.sentIDs()) != 1 {
		t.Errorf("sent = %d messages, want the message delivered", len(fixture.repo.sentIDs()))
	}
	if len(fixture.repo.retryCalls()) != 0 || len(fixture.repo.failedMessages()) != 0 {
		t.Errorf("retries = %d, failures = %d, want neither for a best-effort presence failure",
			len(fixture.repo.retryCalls()), len(fixture.repo.failedMessages()))
	}
	if !strings.Contains(logs.String(), "presence") || !strings.Contains(logs.String(), "websocket write failed") {
		t.Errorf("logs = %q, want the presence failure logged", logs.String())
	}
}

func TestOutboxStartRecoveryRequeuesStuckMessages(t *testing.T) {
	fixture := newOutboxFixture()
	fixture.repo.requeueCount = 3

	fixture.outbox.StartRecovery(context.Background())

	requeues := fixture.repo.requeueCalls()
	if len(requeues) != 1 {
		t.Fatalf("RequeueStuck calls = %d, want 1", len(requeues))
	}
	wantCutoff := fixture.now.Add(-stuckThreshold)
	if !requeues[0].Equal(wantCutoff) {
		t.Errorf("RequeueStuck cutoff = %s, want %s", requeues[0], wantCutoff)
	}
}

func TestOutboxRunRecoversStuckMessages(t *testing.T) {
	fixture := newOutboxFixture()

	runOutbox(t, fixture)

	requeues := fixture.repo.requeueCalls()
	if len(requeues) != 1 {
		t.Fatalf("RequeueStuck calls = %d, want the boot recovery", len(requeues))
	}
	if want := fixture.now.Add(-stuckThreshold); !requeues[0].Equal(want) {
		t.Errorf("RequeueStuck cutoff = %s, want %s", requeues[0], want)
	}
}

func TestOutboxClaimFailureDoesNotStopWorkers(t *testing.T) {
	fixture := newOutboxFixture()
	fixture.repo.claimErr = errors.New("database down")

	runOutbox(t, fixture)

	if len(fixture.repo.claimCallLimits()) == 0 {
		t.Fatal("ClaimQueued was never called")
	}
}

func TestOutboxSendsMediaMessage(t *testing.T) {
	mediaID := uuid.New()
	fixture := newOutboxFixture(model.OutboundMessage{
		Type:         TypeMedia,
		RecipientJID: "5547988359190@s.whatsapp.net",
		Payload:      []byte(`{"caption":"olha","filename":"foto.jpg"}`),
		MediaID:      &mediaID,
		Status:       StatusQueued,
	})
	messageID := fixture.repo.queue[0].ID
	fixture.media.pathFn = func(_ context.Context, id uuid.UUID) (string, *model.Media, error) {
		if id != mediaID {
			t.Errorf("Path id = %s, want %s", id, mediaID)
		}
		return "/data/media/foto", &model.Media{ID: mediaID, Mimetype: "image/jpeg", Filename: "foto.jpg"}, nil
	}

	runOutbox(t, fixture)

	if len(fixture.repo.sentIDs()) != 1 {
		t.Fatalf("sent = %d messages, want the media message delivered", len(fixture.repo.sentIDs()))
	}
	if _, ok := fixture.repo.sentIDs()[messageID]; !ok {
		t.Error("the media message was not marked sent")
	}
	if len(fixture.repo.failedMessages()) != 0 {
		t.Fatalf("failed = %v, want none", fixture.repo.failedMessages())
	}

	calls := fixture.session.SendCalls()
	if len(calls) != 1 {
		t.Fatalf("session sends = %d, want 1", len(calls))
	}
	if calls[0].Type != "image" || calls[0].MediaPath != "/data/media/foto" {
		t.Errorf("session message = %+v, want the image upload from /data/media/foto", calls[0])
	}
}

func TestRetryDelayGrowsAndCaps(t *testing.T) {
	tests := []struct {
		retries int
		want    time.Duration
	}{
		{retries: 0, want: time.Second},
		{retries: 1, want: 2 * time.Second},
		{retries: 2, want: 4 * time.Second},
		{retries: 4, want: 16 * time.Second},
		{retries: 7, want: 2 * time.Minute},
		{retries: 30, want: 2 * time.Minute},
	}

	for _, tt := range tests {
		if got := retryDelay(tt.retries); got != tt.want {
			t.Errorf("retryDelay(%d) = %s, want %s", tt.retries, got, tt.want)
		}
	}
}
