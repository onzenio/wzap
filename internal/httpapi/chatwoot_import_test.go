package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/model"
)

// fakeImporter replays one import count for the manual import route.
type fakeImporter struct {
	mu    sync.Mutex
	count int
	err   error
	calls []uuid.UUID
}

func (f *fakeImporter) ImportHistory(_ context.Context, id uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	return f.count, f.err
}

func (f *fakeImporter) called(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.calls {
		if got == id {
			return true
		}
	}
	return false
}

func chatwootImportTestServer(t *testing.T, instances InstanceService, cfgs *fakeChatwootConfigs, importer ChatwootImporter) *http.Server {
	t.Helper()
	on := config.Chatwoot{Enabled: true}
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: "https://wzap.example.com", Chatwoot: on},
		discardLogger(),
		Deps{
			ReadyChecker:     checkFunc(func(context.Context) error { return nil }),
			Instances:        instances,
			ChatwootConfigs:  cfgs,
			Chatwoot:         on,
			PublicURL:        "https://wzap.example.com",
			ChatwootImporter: importer,
		},
	)
}

// TestChatwootImportReturnsCount pins the manual trigger: POST
// .../chatwoot/import answers 202 with the imported count behind the dual
// auth.
func TestChatwootImportReturnsCount(t *testing.T) {
	id := uuid.New()
	importer := &fakeImporter{count: 3}
	cfgs := &fakeChatwootConfigs{
		getFn: func(_ context.Context, got uuid.UUID) (*model.ChatwootConfig, error) {
			return &model.ChatwootConfig{InstanceID: got, Enabled: true}, nil
		},
	}
	srv := chatwootImportTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, cfgs, importer)

	rec := serveJSON(t, srv, http.MethodPost, "/instances/"+id.String()+"/chatwoot/import", "")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"imported":3`) {
		t.Errorf("body = %s, want the imported count", rec.Body.String())
	}
	if !importer.called(id) {
		t.Errorf("importer not called for instance %s", id)
	}
}

// TestChatwootImportRequiresAuth pins the dual auth: the manual import
// without credential answers 401 like set/find.
func TestChatwootImportRequiresAuth(t *testing.T) {
	id := uuid.New()
	srv := chatwootImportTestServer(t, &fakeInstanceService{}, &fakeChatwootConfigs{}, &fakeImporter{})

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/chatwoot/import", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestChatwootImportGlobalDisabledReturns400 pins the global gate: with the
// connector off the manual import answers 400 without touching the
// importer.
func TestChatwootImportGlobalDisabledReturns400(t *testing.T) {
	id := uuid.New()
	off := config.Chatwoot{Enabled: false}
	importer := &fakeImporter{count: 3}
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: "https://wzap.example.com", Chatwoot: off},
		discardLogger(),
		Deps{
			ReadyChecker:     checkFunc(func(context.Context) error { return nil }),
			Instances:        &fakeInstanceService{},
			ChatwootConfigs:  &fakeChatwootConfigs{},
			Chatwoot:         off,
			PublicURL:        "https://wzap.example.com",
			ChatwootImporter: importer,
		},
	)

	rec := serveJSON(t, srv, http.MethodPost, "/instances/"+id.String()+"/chatwoot/import", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if len(importer.calls) != 0 {
		t.Errorf("importer calls = %d, want 0 when globally disabled", len(importer.calls))
	}
}
