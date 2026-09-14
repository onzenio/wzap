package whatsmeow

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"

	"wzap/internal/session"
)

// connectionEvent is one recorded sink callback.
type connectionEvent struct {
	status session.Status
	jid    string
	reason string
}

// recordingSink collects the connection events emitted by a session.
type recordingSink struct {
	mu       sync.Mutex
	events   []connectionEvent
	messages []session.InboundMessage
	edits    []session.MessageEdit
	deletes  []session.MessageDelete
}

func (r *recordingSink) OnMessage(_ context.Context, msg session.InboundMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, msg)
}

func (r *recordingSink) OnMessageEdit(_ context.Context, edit session.MessageEdit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, edit)
}

func (r *recordingSink) OnMessageDelete(_ context.Context, del session.MessageDelete) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, del)
}

func (r *recordingSink) OnReceipt(context.Context, session.Receipt) {}

func (r *recordingSink) OnConnection(_ context.Context, _ uuid.UUID, status session.Status, jid, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, connectionEvent{status: status, jid: jid, reason: reason})
}

// last returns the most recent connection event or fails the test when none was
// emitted.
func (r *recordingSink) last(t *testing.T) connectionEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		t.Fatal("no connection event was emitted")
	}
	return r.events[len(r.events)-1]
}

// count returns how many connection events were emitted.
func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// messageCount returns how many plain messages were emitted.
func (r *recordingSink) messageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// editCount returns how many message edits were emitted.
func (r *recordingSink) editCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.edits)
}

// deleteCount returns how many message deletes were emitted.
func (r *recordingSink) deleteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deletes)
}

// lastEdit returns the most recent message edit or fails the test when none
// was emitted.
func (r *recordingSink) lastEdit(t *testing.T) session.MessageEdit {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.edits) == 0 {
		t.Fatal("no message edit was emitted")
	}
	return r.edits[len(r.edits)-1]
}

// lastDelete returns the most recent message delete or fails the test when
// none was emitted.
func (r *recordingSink) lastDelete(t *testing.T) session.MessageDelete {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.deletes) == 0 {
		t.Fatal("no message delete was emitted")
	}
	return r.deletes[len(r.deletes)-1]
}

// sleepRecorder records the backoff delays requested by the reconnect loop.
type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delays = append(s.delays, d)
	return nil
}

func (s *sleepRecorder) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.delays)
}

// noJitter keeps the backoff delays deterministic in the transition tests.
func noJitter(d time.Duration) time.Duration { return d }

// testBackoff is the production policy with the jitter removed.
func testBackoff() reconnectPolicy {
	return reconnectPolicy{base: reconnectBaseDelay, max: reconnectMaxDelay, jitter: noJitter}
}

// newTransitionSession returns a paired, connected session whose sleep and
// reconnect attempts are driven by the test.
func newTransitionSession(sink session.EventSink, sleep sleepFunc, reconnect func(context.Context) error) *instanceSession {
	return &instanceSession{
		instanceID:  uuid.New(),
		log:         slog.Default(),
		sink:        sink,
		status:      session.StatusConnected,
		jid:         "5511999999999@s.whatsapp.net",
		paired:      true,
		sleep:       sleep,
		backoff:     testBackoff(),
		reconnectFn: reconnect,
	}
}

// reconnectRunning reports whether a reconnect loop is registered.
func reconnectRunning(sess *instanceSession) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	return sess.reconnectRun != nil
}

// waitForNoReconnect fails the test when the loop is still registered after a
// short grace period.
func waitForNoReconnect(t *testing.T, sess *instanceSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !reconnectRunning(sess) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("reconnect loop still registered")
}

func TestReconnectPolicyDelayGrowsToCap(t *testing.T) {
	policy := reconnectPolicy{base: reconnectBaseDelay, max: reconnectMaxDelay, jitter: noJitter}
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 64 * time.Second, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute,
	}

	for attempt, wantDelay := range want {
		if got := policy.delay(attempt); got != wantDelay {
			t.Errorf("delay(%d) = %v, want %v", attempt, got, wantDelay)
		}
	}
}

func TestReconnectPolicyJitterStaysInWindow(t *testing.T) {
	full := reconnectPolicy{base: reconnectBaseDelay, max: reconnectMaxDelay}
	jittered := reconnectPolicy{base: reconnectBaseDelay, max: reconnectMaxDelay, jitter: defaultJitter}

	for attempt := 0; attempt < 8; attempt++ {
		window := full.delay(attempt)
		for i := 0; i < 50; i++ {
			got := jittered.delay(attempt)
			if got < window/2 || got > window {
				t.Fatalf("attempt %d jittered delay = %v, want within [%v, %v]", attempt, got, window/2, window)
			}
		}
	}
}

func TestTransientDisconnectReconnectsWithGrowingBackoff(t *testing.T) {
	sink := &recordingSink{}
	attempts := make(chan struct{}, 4)
	sleeps := make(chan time.Duration, 4)

	var mu sync.Mutex
	calls := 0
	sess := newTransitionSession(sink,
		func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case sleeps <- d:
				return nil
			}
		},
		func(context.Context) error {
			mu.Lock()
			calls++
			attempt := calls
			mu.Unlock()
			attempts <- struct{}{}
			if attempt < 3 {
				return errors.New("still offline")
			}
			return nil
		})

	sess.dispatch(&events.Disconnected{})

	if got := sess.Status(); got != session.StatusDisconnected {
		t.Fatalf("status = %q, want %q while retrying", got, session.StatusDisconnected)
	}
	if event := sink.last(t); event.status != session.StatusDisconnected {
		t.Fatalf("sink event = %+v, want a disconnected event", event)
	}

	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, wantDelay := range want {
		select {
		case <-attempts:
		case <-time.After(2 * time.Second):
			t.Fatalf("reconnect attempt %d did not run", i+1)
		}
		select {
		case got := <-sleeps:
			if got != wantDelay {
				t.Errorf("sleep before attempt %d = %v, want %v", i+1, got, wantDelay)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sleep before attempt %d did not happen", i+1)
		}
	}

	waitForNoReconnect(t, sess)
	if got := sess.Status(); got != session.StatusDisconnected {
		t.Fatalf("status after a successful attempt = %q, want %q until Connected arrives", got, session.StatusDisconnected)
	}
}

func TestLoggedOutStopsWithoutReconnect(t *testing.T) {
	sink := &recordingSink{}
	sleeps := &sleepRecorder{}
	var reconnects atomic.Int64
	sess := newTransitionSession(sink, sleeps.sleep, func(context.Context) error {
		reconnects.Add(1)
		return nil
	})

	sess.dispatch(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})

	if got := sess.Status(); got != session.StatusDisconnected {
		t.Errorf("status = %q, want %q", got, session.StatusDisconnected)
	}
	if event := sink.last(t); !strings.Contains(event.reason, "logged out") {
		t.Errorf("sink reason = %q, want a logged out reason", event.reason)
	}
	if reconnectRunning(sess) {
		t.Error("logout scheduled a reconnect")
	}
	if got := sleeps.calls(); got != 0 {
		t.Errorf("logout slept %d times, want none", got)
	}
	if got := reconnects.Load(); got != 0 {
		t.Errorf("logout attempted %d reconnects, want none", got)
	}
}

func TestTemporaryBanStopsWithoutReconnect(t *testing.T) {
	sink := &recordingSink{}
	sleeps := &sleepRecorder{}
	var reconnects atomic.Int64
	sess := newTransitionSession(sink, sleeps.sleep, func(context.Context) error {
		reconnects.Add(1)
		return nil
	})

	sess.dispatch(&events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: time.Hour})

	if got := sess.Status(); got != session.StatusError {
		t.Errorf("status = %q, want %q", got, session.StatusError)
	}
	if event := sink.last(t); !strings.Contains(event.reason, "banned") {
		t.Errorf("sink reason = %q, want the ban reason", event.reason)
	}
	if reconnectRunning(sess) {
		t.Error("temporary ban scheduled a reconnect")
	}
	if got := sleeps.calls(); got != 0 {
		t.Errorf("temporary ban slept %d times, want none", got)
	}
	if got := reconnects.Load(); got != 0 {
		t.Errorf("temporary ban attempted %d reconnects, want none", got)
	}
}

func TestTerminalStateIgnoresFollowingDrop(t *testing.T) {
	sink := &recordingSink{}
	sleeps := &sleepRecorder{}
	var reconnects atomic.Int64
	sess := newTransitionSession(sink, sleeps.sleep, func(context.Context) error {
		reconnects.Add(1)
		return nil
	})

	sess.dispatch(&events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: time.Hour})
	sess.dispatch(&events.Disconnected{})

	if got := sess.Status(); got != session.StatusError {
		t.Errorf("status = %q, want the terminal %q", got, session.StatusError)
	}
	if reconnectRunning(sess) {
		t.Error("a drop after a terminal error scheduled a reconnect")
	}
	if got := sleeps.calls(); got != 0 {
		t.Errorf("the session slept %d times after a terminal error, want none", got)
	}
	if got := reconnects.Load(); got != 0 {
		t.Errorf("the session attempted %d reconnects after a terminal error, want none", got)
	}
}

func TestTerminalTransitionCancelsPendingReconnect(t *testing.T) {
	sleepStarted := make(chan struct{})
	sink := &recordingSink{}
	sess := newTransitionSession(sink,
		func(ctx context.Context, _ time.Duration) error {
			close(sleepStarted)
			<-ctx.Done()
			return ctx.Err()
		},
		func(context.Context) error { return nil })

	sess.dispatch(&events.Disconnected{})
	select {
	case <-sleepStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect sleep did not start")
	}

	sess.dispatch(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})

	waitForNoReconnect(t, sess)
	if got := sess.Status(); got != session.StatusDisconnected {
		t.Errorf("status = %q, want %q", got, session.StatusDisconnected)
	}
}

func TestNewSessionDisablesLibraryAutoReconnect(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if sess.client.EnableAutoReconnect {
		t.Error("the whatsmeow client auto-reconnect is enabled; wzap must own the backoff")
	}
}

func TestDisconnectClearsIdentityAndEmitsEvent(t *testing.T) {
	sink := &recordingSink{}
	sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	sess.setStatus(session.StatusConnected, "5511999999999@s.whatsapp.net", "")

	if err := sess.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if got := sess.JID(); got != "" {
		t.Errorf("JID after Disconnect = %q, want empty", got)
	}
	if got := sess.Status(); got != session.StatusDisconnected {
		t.Errorf("status after Disconnect = %q, want %q", got, session.StatusDisconnected)
	}
	event := sink.last(t)
	if event.status != session.StatusDisconnected || event.jid != "" || event.reason != "disconnected by request" {
		t.Errorf("sink event = %+v, want a disconnected event without a JID", event)
	}
	if reconnectRunning(sess) {
		t.Error("Disconnect left a reconnect loop registered")
	}

	if err := sess.remove(context.Background()); err != nil {
		t.Fatalf("remove after Disconnect: %v", err)
	}
	if got := sink.count(); got != 2 {
		t.Errorf("connection events = %d, want 2 (connected then disconnected)", got)
	}
}

func TestDisconnectLogsOutFromWhatsApp(t *testing.T) {
	sink := &recordingSink{}
	sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	logouts := 0
	sess.logoutFn = func(context.Context) error {
		logouts++
		return whatsmeow.ErrNotConnected
	}

	if err := sess.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if logouts != 1 {
		t.Fatalf("logout attempts = %d, want 1", logouts)
	}
}

func TestRemoveLogsOutEvenWhenItFails(t *testing.T) {
	sink := &recordingSink{}
	sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	logouts := 0
	sess.logoutFn = func(context.Context) error {
		logouts++
		return errors.New("whatsapp unreachable")
	}

	if err := sess.remove(context.Background()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if logouts != 1 {
		t.Fatalf("logout attempts = %d, want 1", logouts)
	}
}

func TestRemoveEmitsWhenItChangesStatus(t *testing.T) {
	sink := &recordingSink{}
	sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	sess.setStatus(session.StatusConnected, "5511999999999@s.whatsapp.net", "")

	if err := sess.remove(context.Background()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	event := sink.last(t)
	if event.status != session.StatusDisconnected || event.reason != "session removed" {
		t.Errorf("sink event = %+v, want a session removed event", event)
	}
}
