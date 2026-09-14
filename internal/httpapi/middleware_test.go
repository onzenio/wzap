package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/storage"
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

func TestAuthenticate(t *testing.T) {
	const globalKey = testToken
	const jwtSecret = testJWTSecret

	adminID := uuid.New()
	userID := uuid.New()
	instanceID := uuid.New()

	const instanceKey = "instance-key-in-clear"
	sum := sha256.Sum256([]byte(instanceKey))
	instanceHash := hex.EncodeToString(sum[:])
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{instanceHash: instanceID}}

	adminToken, err := auth.MintToken(adminID, "admin", jwtSecret)
	if err != nil {
		t.Fatalf("MintToken admin: %v", err)
	}
	userToken, err := auth.MintToken(userID, "user", jwtSecret)
	if err != nil {
		t.Fatalf("MintToken user: %v", err)
	}
	expiredAdmin, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": adminID.String(), "role": "admin",
		"exp": time.Now().Add(-time.Hour).Unix(),
	}).SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	wrongSecretToken, err := auth.MintToken(adminID, "admin", "another-secret")
	if err != nil {
		t.Fatalf("MintToken wrong secret: %v", err)
	}

	serve := func(req *http.Request) (*httptest.ResponseRecorder, *auth.Scope) {
		t.Helper()
		var captured auth.Scope
		var found bool
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, found = auth.ScopeFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		rec := httptest.NewRecorder()
		Authenticate(globalKey, nil, keys, jwtSecret)(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec, nil
		}
		if !found {
			t.Fatalf("inner handler runs without a scope in context")
		}
		return rec, &captured
	}

	newRequest := func(cookieValue, apiKey string, bearer string) *http.Request {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		if cookieValue != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})
		}
		if apiKey != "" {
			req.Header.Set("apikey", apiKey)
		}
		if bearer != "" {
			req.Header.Set("Authorization", bearer)
		}
		return req
	}

	t.Run("admin session cookie yields user scope", func(t *testing.T) {
		_, scope := serve(newRequest(adminToken, "", ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeUser || scope.UserID != adminID || scope.Role != "admin" {
			t.Errorf("scope = %+v, want user admin %s", scope, adminID)
		}
	})

	t.Run("user session cookie yields user scope", func(t *testing.T) {
		_, scope := serve(newRequest(userToken, "", ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeUser || scope.UserID != userID || scope.Role != "user" {
			t.Errorf("scope = %+v, want user %s", scope, userID)
		}
	})

	t.Run("global apikey yields global scope", func(t *testing.T) {
		_, scope := serve(newRequest("", globalKey, ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeGlobal {
			t.Errorf("scope kind = %q, want %q", scope.Kind, auth.ScopeGlobal)
		}
	})

	t.Run("instance key yields instance scope", func(t *testing.T) {
		_, scope := serve(newRequest("", instanceKey, ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeInstance || scope.InstanceID != instanceID {
			t.Errorf("scope = %+v, want instance %s", scope, instanceID)
		}
	})

	t.Run("session wins over global key", func(t *testing.T) {
		_, scope := serve(newRequest(userToken, globalKey, ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeUser || scope.UserID != userID {
			t.Errorf("scope = %+v, want session user %s to win", scope, userID)
		}
	})

	t.Run("bad cookie falls through to global key", func(t *testing.T) {
		for name, bad := range map[string]string{
			"malformed":    "not-a-token",
			"expired":      expiredAdmin,
			"wrong secret": wrongSecretToken,
		} {
			t.Run(name, func(t *testing.T) {
				_, scope := serve(newRequest(bad, globalKey, ""))
				if scope == nil {
					t.Fatal("no scope captured")
				}
				if scope.Kind != auth.ScopeGlobal {
					t.Errorf("scope kind = %q, want %q", scope.Kind, auth.ScopeGlobal)
				}
			})
		}
	})

	t.Run("bad cookie falls through to instance key", func(t *testing.T) {
		_, scope := serve(newRequest("not-a-token", instanceKey, ""))
		if scope == nil {
			t.Fatal("no scope captured")
		}
		if scope.Kind != auth.ScopeInstance || scope.InstanceID != instanceID {
			t.Errorf("scope = %+v, want instance %s", scope, instanceID)
		}
	})

	t.Run("failures share identical 401s", func(t *testing.T) {
		unknown := newRequest("", "unknown-key", "")
		none := newRequest("", "", "")
		bearerOnly := newRequest("", "", "Bearer "+globalKey)

		var bodies = map[string]string{}
		for name, req := range map[string]*http.Request{
			"unknown key":   unknown,
			"no credential": none,
			"Bearer only":   bearerOnly,
		} {
			t.Run(name, func(t *testing.T) {
				var reached bool
				inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					reached = true
					w.WriteHeader(http.StatusOK)
				})
				rec := httptest.NewRecorder()
				Authenticate(globalKey, nil, keys, jwtSecret)(inner).ServeHTTP(rec, req)

				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
				}
				if reached {
					t.Error("inner handler ran on an unauthenticated request")
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "unauthorized" {
					t.Errorf("error code = %q, want %q", code, "unauthorized")
				}
				for _, leak := range []string{globalKey, instanceKey, jwtSecret} {
					if leak != "" && strings.Contains(rec.Body.String(), leak) {
						t.Errorf("response leaks a credential: %q", rec.Body.String())
					}
				}
				bodies[name] = rec.Body.String()
			})
		}

		// The middleware must not distinguish missing, unknown and legacy
		// Bearer: all three 401 bodies are byte-for-byte identical.
		if bodies["no credential"] != bodies["unknown key"] {
			t.Errorf("no-credential body differs from unknown-key body:\nno-credential: %s\nunknown-key:   %s",
				bodies["no credential"], bodies["unknown key"])
		}
		if bodies["Bearer only"] != bodies["unknown key"] {
			t.Errorf("Bearer-only body differs from unknown-key body:\nBearer-only: %s\nunknown-key: %s",
				bodies["Bearer only"], bodies["unknown key"])
		}
	})
}

func TestAuthenticateRejectsEmptyGlobalKey(t *testing.T) {
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{}}
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	req.Header.Set("apikey", "some-key")
	rec := httptest.NewRecorder()

	Authenticate("", nil, keys, testJWTSecret)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// fakeAPIKeyRepository is an in-memory storage.APIKeyRepository keyed by hash.
type fakeAPIKeyRepository struct {
	byHash map[string]uuid.UUID
	err    error
}

func (f *fakeAPIKeyRepository) SetHash(_ context.Context, instanceID uuid.UUID, hash string) error {
	if f.err != nil {
		return f.err
	}
	if f.byHash == nil {
		f.byHash = map[string]uuid.UUID{}
	}
	f.byHash[hash] = instanceID
	return nil
}

func (f *fakeAPIKeyRepository) InstanceByHash(_ context.Context, hash string) (uuid.UUID, error) {
	if f.err != nil {
		return uuid.Nil, f.err
	}
	if id, ok := f.byHash[hash]; ok {
		return id, nil
	}
	return uuid.Nil, storage.ErrNotFound
}

func (f *fakeAPIKeyRepository) ClearHash(_ context.Context, instanceID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	for hash, id := range f.byHash {
		if id == instanceID {
			delete(f.byHash, hash)
		}
	}
	return nil
}

func (f *fakeAPIKeyRepository) CountByOwner(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (f *fakeAPIKeyRepository) CountAll(_ context.Context) (int, error) { return 0, nil }

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

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
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

	req := httptest.NewRequest(http.MethodPost, "/instances", nil)
	req.Header.Set("X-Request-Id", "log-correlation")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var entry map[string]any
	decodeJSON(t, bytes.TrimSpace(logged.Bytes()), &entry)

	for key, want := range map[string]any{
		"request_id": "log-correlation",
		"method":     "POST",
		"path":       "/instances",
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
