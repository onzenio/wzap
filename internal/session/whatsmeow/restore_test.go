package whatsmeow

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
)

// TestManagerRestartKeepsPersistedSessions verifies task 4.2: closing the
// manager and opening a new one over the same database (a process restart)
// keeps the paired credentials usable without a new pairing.
func TestManagerRestartKeepsPersistedSessions(t *testing.T) {
	schemaDSN := createIsolatedSchema(t, requireTestDSN(t))
	ctx := context.Background()

	first, err := NewManager(ctx, schemaDSN, nil, slog.Default(), nil, testMediaLimit)
	if err != nil {
		t.Fatalf("NewManager before restart: %v", err)
	}
	jid := saveTestDevice(t, first.devices, "5511999999999")
	if err := first.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}

	second, err := NewManager(ctx, schemaDSN, nil, slog.Default(), nil, testMediaLimit)
	if err != nil {
		t.Fatalf("NewManager after restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	instanceID := uuid.New()
	sess, err := second.Create(&model.Instance{ID: instanceID, WhatsAppJID: jid.String()})
	if err != nil {
		t.Fatalf("Create after restart: %v", err)
	}
	if sess.JID() != jid.String() {
		t.Fatalf("session JID after restart = %q, want persisted %q", sess.JID(), jid.String())
	}
	restored, ok := second.Get(instanceID)
	if !ok {
		t.Fatal("restored session is not registered in the new manager")
	}
	if restored.JID() != jid.String() {
		t.Fatalf("registered session JID = %q, want persisted %q", restored.JID(), jid.String())
	}
}

// TestRestoreAllReconnectsWithoutNewPairing verifies task 4.7: RestoreAll
// brings a persisted session back to connected using the stored credentials,
// without opening a new pairing.
func TestRestoreAllReconnectsWithoutNewPairing(t *testing.T) {
	manager := newTestManager(t)
	jid := saveTestDevice(t, manager.devices, "5511999999999")
	sink := &recordingSink{}
	manager.sink = sink
	instanceID := uuid.New()
	manager.instances = &fakeInstanceRepo{instances: []model.Instance{{
		ID: instanceID, Status: "disconnected", WhatsAppJID: jid.String(),
	}}}

	var mu sync.Mutex
	restoredJIDs := map[string]bool{}
	manager.restoreConnect = func(_ context.Context, sess *instanceSession) error {
		mu.Lock()
		restoredJIDs[sess.JID()] = true
		mu.Unlock()
		// The stub stands in for the network handshake: the session comes
		// from the persisted device, so reaching connected needs no QR.
		sess.setStatus(session.StatusConnected, sess.JID(), "")
		return nil
	}

	if err := manager.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	sess, ok := manager.Get(instanceID)
	if !ok {
		t.Fatal("restored session is not registered in the manager")
	}
	if sess.Status() != session.StatusConnected {
		t.Fatalf("restored session status = %q, want connected", sess.Status())
	}
	if sess.JID() != jid.String() {
		t.Fatalf("restored session JID = %q, want persisted %q", sess.JID(), jid.String())
	}
	mu.Lock()
	restored := restoredJIDs[jid.String()]
	mu.Unlock()
	if !restored {
		t.Fatalf("restore connected device %q, want the persisted JID", jid.String())
	}
	event := sink.last(t)
	if event.status != session.StatusConnected {
		t.Errorf("restore event status = %q, want connected", event.status)
	}
	if event.jid != jid.String() {
		t.Errorf("restore event JID = %q, want %q", event.jid, jid.String())
	}
}
