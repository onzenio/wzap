package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/instance"
	"wzap/internal/model"
)

// apikeyHashOf returns the hex sha256 the middleware resolves, matching
// auth.MintAPIKey storage semantics.
func apikeyHashOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// apikeyTestServer wires svc and keys behind Authenticate so instance-key
// assertions exercise the middleware-shaped resolution (old key → 401, new
// key resolves).
func apikeyTestServer(t *testing.T, svc InstanceService, keys *fakeAPIKeyRepository) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    svc,
			Keys:         keys,
			JWTSecret:    testJWTSecret,
		},
	)
}

// apikeyInstanceService answers Get for known and ErrNotFound otherwise.
func apikeyInstanceService(known ...*model.Instance) *fakeInstanceService {
	byID := map[uuid.UUID]*model.Instance{}
	for _, inst := range known {
		byID[inst.ID] = inst
	}
	return &fakeInstanceService{
		getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
			if inst, ok := byID[id]; ok {
				return inst, nil
			}
			return nil, instance.ErrNotFound
		},
	}
}

// rotateKeyResponse decodes the rotate 200 body.
func rotateKeyResponse(t *testing.T, body []byte) (id, key string) {
	t.Helper()
	var payload struct {
		Data struct {
			ID             string `json:"id"`
			InstanceAPIKey string `json:"instance_api_key"`
		} `json:"data"`
	}
	decodeJSON(t, body, &payload)
	return payload.Data.ID, payload.Data.InstanceAPIKey
}

func TestAPIKeyRotateHappy(t *testing.T) {
	inst := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	const oldKey = "old-instance-key-happy"
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{apikeyHashOf(oldKey): inst.ID}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)

	rec := serveRBAC(t, srv, http.MethodPost, "/instances/"+inst.ID.String()+"/apikey/rotate", "", nil, testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	id, newKey := rotateKeyResponse(t, rec.Body.Bytes())
	if id != inst.ID.String() {
		t.Errorf("data.id = %q, want %s", id, inst.ID)
	}
	if newKey == "" {
		t.Fatal("data.instance_api_key is empty, want the one-time plaintext key")
	}
	if newKey == oldKey {
		t.Error("rotated key equals the old key, want a fresh key")
	}

	// The old hash dies at write: the middleware no longer resolves it.
	if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, oldKey, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("old key status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	// The new key resolves through the middleware to the rotated instance.
	if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, newKey, nil); rec.Code != http.StatusOK {
		t.Errorf("new key status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, err := keys.InstanceByHash(t.Context(), apikeyHashOf(newKey)); err != nil || got != inst.ID {
		t.Errorf("InstanceByHash(new) = %s, %v, want %s with no error", got, err, inst.ID)
	}

	// The key is shown once: no read route exposes instance_api_key.
	get := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, testToken, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d (body %q)", get.Code, http.StatusOK, get.Body.String())
	}
	if strings.Contains(get.Body.String(), "instance_api_key") {
		t.Errorf("get body exposes instance_api_key, want it exactly once at rotate (body %q)", get.Body.String())
	}
}

func TestAPIKeyRotateBackfill(t *testing.T) {
	// A keyless legacy instance gains its first key through the same path.
	inst := &model.Instance{ID: uuid.New(), Name: "legacy", Status: "disconnected"}
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)

	rec := serveRBAC(t, srv, http.MethodPost, "/instances/"+inst.ID.String()+"/apikey/rotate", "", nil, testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	id, newKey := rotateKeyResponse(t, rec.Body.Bytes())
	if id != inst.ID.String() {
		t.Errorf("data.id = %q, want %s", id, inst.ID)
	}
	if newKey == "" {
		t.Fatal("data.instance_api_key is empty on the backfill path")
	}
	if got, err := keys.InstanceByHash(t.Context(), apikeyHashOf(newKey)); err != nil || got != inst.ID {
		t.Errorf("InstanceByHash(new) = %s, %v, want %s with no error", got, err, inst.ID)
	}
	if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, newKey, nil); rec.Code != http.StatusOK {
		t.Errorf("backfilled key status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestAPIKeyRevoke(t *testing.T) {
	inst := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	const oldKey = "old-instance-key-revoke"
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{apikeyHashOf(oldKey): inst.ID}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)
	path := "/instances/" + inst.ID.String() + "/apikey"

	rec := serveRBAC(t, srv, http.MethodDelete, path, "", nil, testToken, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want %d (body %q)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("revoke body = %q, want empty", rec.Body.String())
	}

	// The revoked key no longer authenticates.
	if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, oldKey, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	// The instance answers only to the global key until the next rotation.
	if rec := serveRBAC(t, srv, http.MethodGet, "/instances/"+inst.ID.String(), "", nil, testToken, nil); rec.Code != http.StatusOK {
		t.Errorf("global get after revoke status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Revoking an already-keyless instance still succeeds: idempotent 204.
	if rec := serveRBAC(t, srv, http.MethodDelete, path, "", nil, testToken, nil); rec.Code != http.StatusNoContent {
		t.Errorf("second revoke status = %d, want %d (body %q)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

func TestAPIKeyRotateRevokeForbidden(t *testing.T) {
	userID := uuid.New()
	adminID := uuid.New()
	inst := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	const ownKey = "own-instance-key-forbidden"
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{apikeyHashOf(ownKey): inst.ID}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)

	userToken, err := auth.MintToken(userID, "user", testJWTSecret)
	if err != nil {
		t.Fatalf("MintToken user: %v", err)
	}
	adminToken, err := auth.MintToken(adminID, "admin", testJWTSecret)
	if err != nil {
		t.Fatalf("MintToken admin: %v", err)
	}

	rotatePath := "/instances/" + inst.ID.String() + "/apikey/rotate"
	revokePath := "/instances/" + inst.ID.String() + "/apikey"

	t.Run("self-rotate by instance key is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, rotatePath, "", nil, ownKey, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("revoke by instance key is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodDelete, revokePath, "", nil, ownKey, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("rotate by user session is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, rotatePath, "", rbacSessionCookie(userToken), "", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("revoke by user session is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodDelete, revokePath, "", rbacSessionCookie(userToken), "", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("rotate by admin session succeeds", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, rotatePath, "", rbacSessionCookie(adminToken), "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		if _, key := rotateKeyResponse(t, rec.Body.Bytes()); key == "" {
			t.Error("admin rotate response is missing the one-time key")
		}
	})
}

func TestAPIKeyRotateRevokeNotFound(t *testing.T) {
	unknown := uuid.New()
	svc := &fakeInstanceService{
		getFn: func(context.Context, uuid.UUID) (*model.Instance, error) { return nil, instance.ErrNotFound },
	}
	// The instance-key credential must resolve through the middleware (401
	// otherwise) so the handler can answer 404 after loading: map a live key
	// for an unrelated instance.
	const otherKey = "other-live-instance-key"
	otherID := uuid.New()
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{apikeyHashOf(otherKey): otherID}}
	srv := apikeyTestServer(t, svc, keys)

	userToken, err := auth.MintToken(uuid.New(), "user", testJWTSecret)
	if err != nil {
		t.Fatalf("MintToken user: %v", err)
	}

	// Load-then-404 ordering: user/global scopes see 404 on a random id, while
	// an instance key for another instance is denied 403 before the load.
	t.Run("rotate unknown is not found", func(t *testing.T) {
		for name, tc := range map[string]struct {
			cookie *http.Cookie
			apiKey string
		}{
			"global": {apiKey: testToken},
			"user":   {cookie: rbacSessionCookie(userToken)},
		} {
			t.Run(name, func(t *testing.T) {
				rec := serveRBAC(t, srv, http.MethodPost, "/instances/"+unknown.String()+"/apikey/rotate", "", tc.cookie, tc.apiKey, nil)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
					t.Errorf("error code = %q, want %q", code, "not_found")
				}
			})
		}
		t.Run("instance key", func(t *testing.T) {
			rec := serveRBAC(t, srv, http.MethodPost, "/instances/"+unknown.String()+"/apikey/rotate", "", nil, otherKey, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
				t.Errorf("error code = %q, want %q", code, "forbidden")
			}
		})
	})

	t.Run("revoke unknown is not found", func(t *testing.T) {
		for name, tc := range map[string]struct {
			cookie *http.Cookie
			apiKey string
		}{
			"global": {apiKey: testToken},
			"user":   {cookie: rbacSessionCookie(userToken)},
		} {
			t.Run(name, func(t *testing.T) {
				rec := serveRBAC(t, srv, http.MethodDelete, "/instances/"+unknown.String()+"/apikey", "", tc.cookie, tc.apiKey, nil)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
					t.Errorf("error code = %q, want %q", code, "not_found")
				}
			})
		}
		t.Run("instance key", func(t *testing.T) {
			rec := serveRBAC(t, srv, http.MethodDelete, "/instances/"+unknown.String()+"/apikey", "", nil, otherKey, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
				t.Errorf("error code = %q, want %q", code, "forbidden")
			}
		})
	})
}
