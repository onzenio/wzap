package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/instance"
	"wzap/internal/model"
	"wzap/internal/webhook"
)

// TestCreateInstanceCachesPlaintextKey pins the create hook: after a 201 the
// just-minted plaintext is in the process-local key cache for delivery.
func TestCreateInstanceCachesPlaintextKey(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{
		oldestAdminFn: func(context.Context) (uuid.UUID, error) { return uuid.New(), nil },
		createFn: func(_ context.Context, input instance.CreateInput) (*model.Instance, string, error) {
			return &model.Instance{ID: id, Name: input.Name, Status: "disconnected", OwnerUserID: input.OwnerUserID}, "hook-key-create", nil
		},
	}
	srv := createOwnerTestServer(t, svc)
	t.Cleanup(func() { webhook.Keys.Clear(id) })

	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, testToken, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if got, ok := webhook.Keys.Get(id); !ok || got != "hook-key-create" {
		t.Errorf("cached key = %q, %v, want %q, true", got, ok, "hook-key-create")
	}
}

// TestRotateAPIKeyCachesPlaintextKey pins the rotate hook: after a 200 the
// fresh plaintext replaces whatever the cache held.
func TestRotateAPIKeyCachesPlaintextKey(t *testing.T) {
	inst := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)
	t.Cleanup(func() { webhook.Keys.Clear(inst.ID) })

	webhook.Keys.Store(inst.ID, "stale-key")
	rec := serveRBAC(t, srv, http.MethodPost, "/instances/"+inst.ID.String()+"/apikey/rotate", "", nil, testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	_, fresh := rotateKeyResponse(t, rec.Body.Bytes())
	if got, ok := webhook.Keys.Get(inst.ID); !ok || got != fresh {
		t.Errorf("cached key after rotate matches fresh = %v, want the rotated plaintext cached", ok && got == fresh)
	}
}

// TestRevokeAPIKeyClearsCachedKey pins the revoke hook: after a 204 the cache
// holds nothing for the instance.
func TestRevokeAPIKeyClearsCachedKey(t *testing.T) {
	inst := &model.Instance{ID: uuid.New(), Name: "loja", Status: "disconnected"}
	keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{}}
	srv := apikeyTestServer(t, apikeyInstanceService(inst), keys)

	webhook.Keys.Store(inst.ID, "revoked-key")
	t.Cleanup(func() { webhook.Keys.Clear(inst.ID) })
	rec := serveRBAC(t, srv, http.MethodDelete, "/instances/"+inst.ID.String()+"/apikey", "", nil, testToken, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if _, ok := webhook.Keys.Get(inst.ID); ok {
		t.Error("cached key survives revoke, want it cleared")
	}
}

// TestDeleteInstanceClearsCachedKey pins the delete hygiene hook: removing an
// instance evicts its cached key so a reused UUID never inherits it.
func TestDeleteInstanceClearsCachedKey(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{
		getFn: func(_ context.Context, _ uuid.UUID) (*model.Instance, error) {
			return &model.Instance{ID: id, Name: "loja", Status: "disconnected"}, nil
		},
	}

	webhook.Keys.Store(id, "deleted-key")
	t.Cleanup(func() { webhook.Keys.Clear(id) })
	rec := serveJSON(t, instancesServer(t, svc), http.MethodDelete, "/instances/"+id.String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if _, ok := webhook.Keys.Get(id); ok {
		t.Error("cached key survives instance delete, want it cleared")
	}
}
