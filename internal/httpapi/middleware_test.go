package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const testToken = "test-service-token"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeJSON(t, body, &payload)
	return payload.Error.Code
}

func TestAuth(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "missing header", header: "", wantStatus: http.StatusUnauthorized},
		{name: "missing credentials", header: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic dGVzdA==", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer other-token", wantStatus: http.StatusUnauthorized},
		{name: "extended token", header: "Bearer " + testToken + "-extra", wantStatus: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer " + testToken, wantStatus: http.StatusOK},
		{name: "scheme is case insensitive", header: "bearer " + testToken, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			Auth(testToken)(okHandler()).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusUnauthorized {
				return
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "unauthorized" {
				t.Errorf("error code = %q, want %q", code, "unauthorized")
			}
			if strings.Contains(rec.Body.String(), testToken) {
				t.Errorf("response leaks the service token: %q", rec.Body.String())
			}
		})
	}
}

func TestAuthRejectsEmptyConfiguredToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()

	Auth("")(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequestIDEchoesProvidedID(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "caller-id-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "caller-id-123" {
		t.Errorf("response X-Request-Id = %q, want %q", got, "caller-id-123")
	}
	if seen != "caller-id-123" {
		t.Errorf("context request id = %q, want %q", seen, "caller-id-123")
	}
}

func TestRequestIDGeneratesMissingID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	RequestID(okHandler()).ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-Id")
	if id == "" {
		t.Fatal("response is missing X-Request-Id")
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("generated request id %q is not a uuid: %v", id, err)
	}
}

func TestRecoverReturnsInternalErrorEnvelope(t *testing.T) {
	handler := RequestID(Recover(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: database credentials are hunter2")
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "internal_error" {
		t.Errorf("error code = %q, want %q", code, "internal_error")
	}

	body := rec.Body.String()
	if strings.Contains(body, "hunter2") {
		t.Errorf("response leaks the panic value: %q", body)
	}
	for _, leak := range []string{"goroutine ", ".go:", "runtime/debug"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaks the stack (%q): %q", leak, body)
		}
	}
}

func TestRecoverPassesThroughNormalResponses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	Recover(discardLogger())(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestLoggingRecordsRequestFields(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	handler := RequestID(Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusCreated, map[string]string{"id": "abc"})
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", nil)
	req.Header.Set("X-Request-Id", "log-correlation")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	decodeJSON(t, bytes.TrimSpace(logged.Bytes()), &entry)

	for key, want := range map[string]any{
		"request_id": "log-correlation",
		"method":     "POST",
		"path":       "/api/v1/instances",
		"status":     float64(http.StatusCreated),
	} {
		if entry[key] != want {
			t.Errorf("log field %s = %v, want %v", key, entry[key], want)
		}
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Error("log entry is missing duration_ms")
	}
}
