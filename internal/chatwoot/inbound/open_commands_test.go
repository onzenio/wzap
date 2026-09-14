package inbound

import (
	"context"
	"testing"

	"wzap/internal/session/sessiontest"
)

// RED: webhook aberto nunca executa comandos operacionais (status/init/
// clearcache/disconnect) — descarta 200 sem efeitos.
func TestOpenWebhookDiscardsOperationalWithoutEffect(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(7, "disconnect")
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier
	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200 (discard)", status)
	}
	for _, sess := range []*sessiontest.FakeSession{func() *sessiontest.FakeSession {
		s, _ := fx.sessions.Get(fx.instance)
		if fs, ok := s.(*sessiontest.FakeSession); ok {
			return fs
		}
		return nil
	}()} {
		if sess == nil {
			continue
		}
		if got := sess.DisconnectCalls(); got != 0 {
			t.Errorf("disconnects = %d, want 0 (aberto não desconecta)", got)
		}
	}
	if len(fx.chats.creates) != 0 {
		t.Errorf("posts = %d, want 0 (aberto não confirma comando)", len(fx.chats.creates))
	}
}
