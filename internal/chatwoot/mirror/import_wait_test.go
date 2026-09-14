package mirror

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RED: auto-import deve ser rastreado por WaitGroup para o shutdown esperar.
func TestAutoImportWaitTracksDetached(t *testing.T) {
	release := make(chan struct{})
	fx := newFixture(nil)
	fx.worker.importTrigger = func(context.Context, uuid.UUID) error {
		<-release
		return nil
	}
	id := uuid.New()
	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}
	if err := fx.worker.HandleConnection(context.Background(), id, uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fx.worker.Wait() }()
	select {
	case <-done:
		t.Fatal("Wait retornou com auto-import ainda rodando, want bloqueio")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait não retornou após o import terminar")
	}
}
