package httpapi

import (
	"errors"
	"net/http"

	"wzap/internal/auth"
	"wzap/internal/instance"
	"wzap/internal/storage"
)

// rotateAPIKeyResponse is the 200 answer to a rotation: the instance id with
// its one-time plaintext key. The key travels in this response only and is
// never persisted; only its hash reaches the repository.
type rotateAPIKeyResponse struct {
	ID             string `json:"id"`
	InstanceAPIKey string `json:"instance_api_key"`
}

// handleRotateAPIKey mints a fresh instance key and stores its hash,
// overwriting any prior hash so the old key dies at write. It loads the
// target first (404) and then requires the global scope or an admin session
// (403), so an instance key — even the instance's own — cannot rotate its
// own key. Rotating a keyless legacy instance issues its first key through
// the same path; no rotation history is kept.
func handleRotateAPIKey(instances InstanceService, keys storage.APIKeyRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := requireAdminScope(r); err != nil {
			writeForbidden(w, r)
			return
		}

		key, hash, err := auth.MintAPIKey()
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if keys == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if err := keys.SetHash(r.Context(), stored.ID, hash); err != nil {
			writeKeyError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, rotateAPIKeyResponse{ID: stored.ID.String(), InstanceAPIKey: key})
	}
}

// handleRevokeAPIKey revokes the instance key and answers 204. It loads the
// target first (404) and then requires the global scope or an admin session
// (403). After revoke the instance answers only to the global key and admin
// sessions until the next rotation. Revoking an already-keyless instance
// still succeeds.
func handleRevokeAPIKey(instances InstanceService, keys storage.APIKeyRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := requireAdminScope(r); err != nil {
			writeForbidden(w, r)
			return
		}

		if keys == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if err := keys.ClearHash(r.Context(), stored.ID); err != nil {
			writeKeyError(w, r, err)
			return
		}
		JSON(w, http.StatusNoContent, nil)
	}
}

// requireAdminScope reports whether the request acts as the global scope or
// an admin session. Instance scopes and non-admin user sessions are denied.
func requireAdminScope(r *http.Request) error {
	scope, ok := auth.ScopeFromContext(r.Context())
	if !ok {
		return auth.ErrForbidden
	}
	return auth.RequireRole(scope, "admin")
}

// writeKeyError maps a key repository failure to its HTTP status: a missing
// instance answers 404, anything else a 500 without leaking its cause.
func writeKeyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, instance.ErrNotFound), errors.Is(err, storage.ErrNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
