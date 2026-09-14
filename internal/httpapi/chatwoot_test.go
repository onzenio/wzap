package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/inbound"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

type fakeChatwootConfigs struct {
	getFn func(ctx context.Context, id uuid.UUID) (*model.ChatwootConfig, error)
	putFn func(ctx context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error)
	puts  []model.ChatwootConfig
}

func (f *fakeChatwootConfigs) Get(ctx context.Context, id uuid.UUID) (*model.ChatwootConfig, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return nil, storage.ErrNotFound
}

func (f *fakeChatwootConfigs) Put(ctx context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error) {
	f.puts = append(f.puts, cfg)
	if f.putFn != nil {
		return f.putFn(ctx, cfg)
	}
	return &cfg, nil
}

func (f *fakeChatwootConfigs) Delete(ctx context.Context, id uuid.UUID) error { return nil }

func chatwootTestServer(t *testing.T, instances InstanceService, cfgs *fakeChatwootConfigs, chatwoot config.Chatwoot) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, PublicURL: "https://wzap.example.com", Chatwoot: chatwoot},
		discardLogger(),
		Deps{
			ReadyChecker:    checkFunc(func(context.Context) error { return nil }),
			Instances:       instances,
			ChatwootConfigs: cfgs,
			Chatwoot:        chatwoot,
			PublicURL:       "https://wzap.example.com",
		},
	)
}

func chatwootOn() config.Chatwoot { return config.Chatwoot{Enabled: true} }

func TestChatwootSetRequiresAuth(t *testing.T) {
	id := uuid.New()
	srv := chatwootTestServer(t, &fakeInstanceService{}, &fakeChatwootConfigs{}, chatwootOn())

	req := httptest.NewRequest(http.MethodPut, "/instances/"+id.String()+"/chatwoot", strings.NewReader(`{"enabled":false}`))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestChatwootSetBehindDualAuth(t *testing.T) {
	id := uuid.New()
	cfgs := &fakeChatwootConfigs{}
	srv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, cfgs, chatwootOn())

	rec := serveJSON(t, srv, http.MethodPut, "/instances/"+id.String()+"/chatwoot", `{"enabled":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(cfgs.puts) != 1 {
		t.Fatalf("Put calls = %d, want 1", len(cfgs.puts))
	}
}

func TestChatwootSetValidation422(t *testing.T) {
	id := uuid.New()
	cfgs := &fakeChatwootConfigs{}
	srv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, cfgs, chatwootOn())

	rec := serveJSON(t, srv, http.MethodPut, "/instances/"+id.String()+"/chatwoot", `{"enabled":true,"url":"","account_id":"","token":""}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unprocessable_entity" {
		t.Errorf("error code = %q, want unprocessable_entity", code)
	}
	if len(cfgs.puts) != 0 {
		t.Errorf("Put calls = %d, want 0 on validation failure", len(cfgs.puts))
	}
}

func TestChatwootSetAutoCreateReturnsWebhookURL(t *testing.T) {
	id := uuid.New()
	cfgs := &fakeChatwootConfigs{}
	srv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, cfgs, chatwootOn())

	body := `{"enabled":true,"url":"https://chatwoot.example.com","account_id":"1","token":"secret","auto_create":true}`
	rec := serveJSON(t, srv, http.MethodPut, "/instances/"+id.String()+"/chatwoot", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "webhook_url") {
		t.Errorf("response misses webhook_url: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/chatwoot/webhook/"+id.String()) {
		t.Errorf("webhook_url misses instance path: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "auto_create") {
		t.Errorf("response misses auto_create: %s", rec.Body.String())
	}
}

func TestChatwootGetWithoutConfigReturnsDisabled(t *testing.T) {
	id := uuid.New()
	srv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, &fakeChatwootConfigs{}, chatwootOn())

	rec := serveJSON(t, srv, http.MethodGet, "/instances/"+id.String()+"/chatwoot", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("response misses disabled default: %s", rec.Body.String())
	}
}

func TestChatwootGlobalDisabledReturns400(t *testing.T) {
	id := uuid.New()
	off := config.Chatwoot{Enabled: false}
	srv := chatwootTestServer(t, &fakeInstanceService{}, &fakeChatwootConfigs{}, off)

	rec := serveJSON(t, srv, http.MethodPut, "/instances/"+id.String()+"/chatwoot", `{"enabled":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = serveJSON(t, srv, http.MethodGet, "/instances/"+id.String()+"/chatwoot", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestChatwootWebhookOutsideAuth(t *testing.T) {
	id := uuid.New()
	srv := chatwootTestServer(t, &fakeInstanceService{}, &fakeChatwootConfigs{}, config.Chatwoot{Enabled: false})

	// Webhook is open by design: no credential must not answer 401.
	req := httptest.NewRequest(http.MethodPost, "/chatwoot/webhook/"+id.String(), strings.NewReader(`{"event":"message_created"}`))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("webhook answered 401, want open route (got %d)", rec.Code)
	}
}

// fakeChatwootInbound replays one webhook status for the status-code test.
type fakeChatwootInbound struct {
	status int
	err    error
}

func (f *fakeChatwootInbound) Handle(_ context.Context, _ uuid.UUID, _ inbound.Payload) (int, error) {
	return f.status, f.err
}

// TestChatwootTokenMasked pins that PUT/GET .../chatwoot never echo the
// token: it is accepted on write only, responses carry an empty token.
func TestChatwootTokenMasked(t *testing.T) {
	id := uuid.New()
	cfgs := &fakeChatwootConfigs{}
	srv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, cfgs, chatwootOn())

	rec := serveJSON(t, srv, http.MethodPut, "/instances/"+id.String()+"/chatwoot",
		`{"enabled":true,"url":"https://chatwoot.example.com","account_id":"1","token":"secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var putBody struct {
		Data chatwootConfigResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &putBody); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if putBody.Data.Token != "" {
		t.Errorf("PUT token = %q, want empty (write-only)", putBody.Data.Token)
	}
	if len(cfgs.puts) != 1 || cfgs.puts[0].Token != "secret" {
		t.Errorf("stored token = %v, want the write to persist the secret", cfgs.puts)
	}

	getCfgs := &fakeChatwootConfigs{
		getFn: func(_ context.Context, got uuid.UUID) (*model.ChatwootConfig, error) {
			return &model.ChatwootConfig{InstanceID: got, Enabled: true, URL: "https://chatwoot.example.com", AccountID: "1", Token: "secret", IgnoreJIDs: []string{}}, nil
		},
	}
	getSrv := chatwootTestServer(t, &fakeInstanceService{
		getFn: func(_ context.Context, got uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: got, Name: "loja", Status: "connected"}, nil
		},
	}, getCfgs, chatwootOn())
	rec = serveJSON(t, getSrv, http.MethodGet, "/instances/"+id.String()+"/chatwoot", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var getBody struct {
		Data chatwootConfigResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getBody); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getBody.Data.Token != "" {
		t.Errorf("GET token = %q, want empty (write-only)", getBody.Data.Token)
	}
}

// TestChatwootWebhookKeepsHandlerStatusCodes pins that only the 400
// global-off gate answers chatwoot_disabled: any other inbound status keeps
// its own code.
func TestChatwootWebhookKeepsHandlerStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		handlerErr error
		wantStatus int
		wantCode   string
	}{
		{name: "bad request keeps disabled", status: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantCode: "chatwoot_disabled"},
		{name: "not found keeps not_found", status: http.StatusNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "internal keeps internal_error", status: http.StatusInternalServerError, wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
					ChatwootInbound: &fakeChatwootInbound{status: tc.status, err: tc.handlerErr},
					Chatwoot:        chatwootOn(),
					PublicURL:       "https://wzap.example.com",
				},
			)
			id := uuid.New()
			req := httptest.NewRequest(http.MethodPost, "/chatwoot/webhook/"+id.String(), strings.NewReader(`{"event":"message_created"}`))
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != tc.wantCode {
				t.Errorf("error code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}
