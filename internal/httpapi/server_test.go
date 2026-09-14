package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wzap/internal/config"
)

func newTestServer(t *testing.T) *http.Server {
	t.Helper()
	return New(config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken}, discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    &fakeInstanceService{},
		})
}

func serve(t *testing.T, srv *http.Server, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("apikey", token)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func dataField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	decodeJSON(t, body, &payload)

	var value string
	if err := json.Unmarshal(payload.Data[field], &value); err != nil {
		t.Fatalf("decode data.%s of %q: %v", field, body, err)
	}
	return value
}

func TestNewHealthEndpoints(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/healthz", want: "ok"},
		{path: "/readyz", want: "ready"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := serve(t, newTestServer(t), http.MethodGet, tt.path, "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if got := rec.Header().Get("X-Request-Id"); got == "" {
				t.Error("response is missing X-Request-Id")
			}
			if got := dataField(t, rec.Body.Bytes(), "status"); got != tt.want {
				t.Errorf("data.status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewAPIGroupRequiresAuth(t *testing.T) {
	srv := newTestServer(t)

	t.Run("missing token", func(t *testing.T) {
		rec := serve(t, srv, http.MethodGet, "/api/v1/instances", "")

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "unauthorized" {
			t.Errorf("error code = %q, want %q", code, "unauthorized")
		}
	})

	t.Run("valid token reaches instance handlers", func(t *testing.T) {
		rec := serve(t, srv, http.MethodGet, "/api/v1/instances", testToken)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

func TestAPIFallbackAnswersErrorEnvelope(t *testing.T) {
	srv := newTestServer(t)

	t.Run("unknown path", func(t *testing.T) {
		rec := serve(t, srv, http.MethodGet, "/api/v1/unknown", testToken)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
			t.Errorf("error code = %q, want %q", code, "not_found")
		}
	})

	t.Run("unknown path without token is unauthorized", func(t *testing.T) {
		rec := serve(t, srv, http.MethodGet, "/api/v1/unknown", "")

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("wrong method on a known path", func(t *testing.T) {
		rec := serve(t, srv, http.MethodDelete, "/api/v1/instances", testToken)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "method_not_allowed" {
			t.Errorf("error code = %q, want %q", code, "method_not_allowed")
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, POST" {
			t.Errorf("Allow = %q, want %q", allow, "GET, POST")
		}
	})
}

func TestNewReturnsConfiguredServer(t *testing.T) {
	srv := New(config.Config{HTTPAddr: "127.0.0.1:9999", APIKey: testToken}, discardLogger(), Deps{})

	if srv.Addr != "127.0.0.1:9999" {
		t.Errorf("Addr = %q, want %q", srv.Addr, "127.0.0.1:9999")
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be positive")
	}
}

func TestJSONNoContentHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()

	JSON(rec, http.StatusNoContent, nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestErrorEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()

	Error(rec, req, http.StatusConflict, "conflict", "external ref already taken")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Error.Code != "conflict" {
		t.Errorf("error code = %q, want %q", payload.Error.Code, "conflict")
	}
	if payload.Error.Message != "external ref already taken" {
		t.Errorf("error message = %q, want %q", payload.Error.Message, "external ref already taken")
	}
}

func TestErrorEchoesRequestIDFromContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "ctx-id-7"))
	rec := httptest.NewRecorder()

	Error(rec, req, http.StatusBadRequest, "invalid_request", "bad input")

	if got := rec.Header().Get("X-Request-Id"); got != "ctx-id-7" {
		t.Errorf("X-Request-Id = %q, want %q", got, "ctx-id-7")
	}
}
