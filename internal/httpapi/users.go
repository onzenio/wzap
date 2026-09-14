package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"wzap/internal/storage"
)

// userQuotaResponse is the JSON representation of a user after a quota edit:
// the minimal shape of task 3.3, which task 3.4 may extend with the remaining
// users CRUD. The password hash never leaves the storage boundary.
type userQuotaResponse struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	InstanceQuota int    `json:"instance_quota"`
}

// patchQuotaRequest is the PATCH /users/{id} payload. The quota arrives as a
// raw message so a non-integer value (string, float, boolean, null) can be
// rejected with 422 instead of the generic 400 of a body decoding failure.
type patchQuotaRequest struct {
	InstanceQuota *json.RawMessage `json:"instance_quota"`
}

// handleUpdateUserQuota edits the per-user instance quota and answers 200
// with the updated user. Only the global scope and admin sessions may edit;
// any other scope answers 403 before the target is read, so non-admin callers
// cannot probe user ids. A malformed id and an unknown user answer 404; a
// missing, non-integer or negative quota answers 422. Unknown JSON fields are
// ignored, matching the shared body decoder.
func handleUpdateUserQuota(users storage.UserRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := requireAdminScope(r); err != nil {
			writeForbidden(w, r)
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			Error(w, r, http.StatusNotFound, "not_found", "user not found")
			return
		}

		var request patchQuotaRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}
		if request.InstanceQuota == nil {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid instance_quota")
			return
		}
		var quota *int
		if err := json.Unmarshal(*request.InstanceQuota, &quota); err != nil || quota == nil || *quota < 0 {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid instance_quota")
			return
		}

		if users == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if err := users.UpdateQuota(r.Context(), id, *quota); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				Error(w, r, http.StatusNotFound, "not_found", "user not found")
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}

		updated, err := users.GetByID(r.Context(), id)
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		JSON(w, http.StatusOK, userQuotaResponse{
			ID:            updated.ID.String(),
			Email:         updated.Email,
			Role:          updated.Role,
			InstanceQuota: updated.InstanceQuota,
		})
	}
}
