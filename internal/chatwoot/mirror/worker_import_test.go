package mirror

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestHandleConnectionAutoImportsOnceAfterPairing pins the post-pairing
// trigger: the first connected notice fires the import, later connected
// notices (even throttled ones) do not fire again.
func TestHandleConnectionAutoImportsOnceAfterPairing(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	fx := newFixture(nil)
	fx.worker.importTrigger = func(context.Context, uuid.UUID) error {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return nil
	}

	instanceID := uuid.New()
	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}
	if err := fx.worker.HandleConnection(context.Background(), instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	// A second connected notice stays single-fire even though the throttle
	// suppresses its post.
	if err := fx.worker.HandleConnection(context.Background(), instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection second: %v", err)
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("import calls = %d, want exactly 1 (auto post-pairing once)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("import calls = %d, want exactly 1", calls)
	}
}

// TestHandleConnectionAutoImportNeverBlocksNotice pins the fire-and-forget:
// a stuck import does not hold the connection handler up.
func TestHandleConnectionAutoImportNeverBlocksNotice(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	fx := newFixture(nil)
	fx.worker.importTrigger = func(context.Context, uuid.UUID) error {
		<-stuck
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(),
			ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleConnection: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HandleConnection blocked on the import trigger, want fire-and-forget")
	}
}

// TestHandleConnectionAutoImportFailureWarnsOnly pins that a failing import
// never fails the connection notice itself.
func TestHandleConnectionAutoImportFailureWarnsOnly(t *testing.T) {
	fx := newFixture(nil)
	fx.worker.importTrigger = func(context.Context, uuid.UUID) error {
		return errors.New("chatwoot import down")
	}

	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(),
		ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}); err != nil {
		t.Fatalf("HandleConnection: %v, want nil despite the import failure", err)
	}
	if calls := fx.cli.creates(); len(calls) != 1 {
		t.Fatalf("operational notices = %d, want 1 (import failure must not disturb the notice)", len(calls))
	}
}

// TestHandleConnectionNonConnectedSkipsAutoImport pins that only the
// connected transition arms the post-pairing import.
func TestHandleConnectionNonConnectedSkipsAutoImport(t *testing.T) {
	fx := newFixture(nil)
	fx.worker.importTrigger = func(context.Context, uuid.UUID) error {
		t.Error("import trigger fired for a non-connected notice")
		return nil
	}

	notice := ConnectionNotice{Status: "pairing", PairingCode: "ABCD-1234"}
	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
}

// TestNotifyOperationalPostsPTBRNotice pins the operational poster the
// import triggers use for their start/result notices.
func TestNotifyOperationalPostsPTBRNotice(t *testing.T) {
	fx := newFixture(nil)
	instanceID := uuid.New()

	if err := fx.worker.NotifyOperational(context.Background(), instanceID, "⏳ Importação do histórico iniciada."); err != nil {
		t.Fatalf("NotifyOperational: %v", err)
	}

	calls := fx.cli.creates()
	if len(calls) != 1 {
		t.Fatalf("operational posts = %d, want 1", len(calls))
	}
	if got := calls[0].req.Content; got != "⏳ Importação do histórico iniciada." {
		t.Errorf("operational content = %q, want the notice text", got)
	}
}
