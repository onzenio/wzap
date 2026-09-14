package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/inbound"
	"wzap/internal/config"
	"wzap/internal/model"
)

// RED: open webhook tem rate-limit por instância (429 após estouro).
func TestChatwootWebhookRateLimited(t *testing.T) {
	id := uuid.New()
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: "https://wzap.example.com", Chatwoot: chatwootOn()},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances: &fakeInstanceService{
				getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
					return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
				},
			},
			ChatwootInbound:        &fakeChatwootInbound{status: 200},
			Chatwoot:               chatwootOn(),
			PublicURL:              "https://wzap.example.com",
			ChatwootWebhookLimiter: NewChatwootRateLimiter(2, 0),
		},
	)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/chatwoot/webhook/"+id.String(), strings.NewReader(`{"event":"message_created","message":{"id":1,"message_type":"incoming"}}`))
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d status = %d, want 200", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/chatwoot/webhook/"+id.String(), strings.NewReader(`{"event":"message_created"}`))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 rate_limited", rec.Code)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", code)
	}
}

// RED: comandos operacionais via rota autenticada executam; rota exige auth.
func TestChatwootCommandAuthenticated(t *testing.T) {
	id := uuid.New()
	cmd := &fakeChatwootCommander{status: 200}
	cfgs := &fakeChatwootConfigs{
		getFn: func(_ context.Context, got uuid.UUID) (*model.ChatwootConfig, error) {
			return &model.ChatwootConfig{InstanceID: got, Enabled: true}, nil
		},
	}
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: "https://wzap.example.com", Chatwoot: chatwootOn()},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances: &fakeInstanceService{
				getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
					return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
				},
			},
			ChatwootInbound: cmd,
			ChatwootConfigs: cfgs,
			Chatwoot:        chatwootOn(),
			PublicURL:       "https://wzap.example.com",
		},
	)
	// Sem credencial: 401.
	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/chatwoot/command", strings.NewReader(`{"command":"status","conversation_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem cred status = %d, want 401", rec.Code)
	}
	// Com global key: executa.
	req = httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/chatwoot/command", strings.NewReader(`{"command":"status","conversation_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", testToken)
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if cmd.calls != 1 || cmd.command != "status" {
		t.Errorf("commander calls=%d cmd=%q, want 1 status", cmd.calls, cmd.command)
	}
}

type fakeChatwootCommander struct {
	fakeChatwootInbound
	calls         int
	command       string
	conversation  int64
	status        int
}

func (f *fakeChatwootCommander) HandleCommand(_ context.Context, _ uuid.UUID, command string, conversationID int64) (int, error) {
	f.calls++
	f.command = command
	f.conversation = conversationID
	if f.status == 0 {
		return 200, nil
	}
	return f.status, nil
}

var _ = inbound.Payload{}
