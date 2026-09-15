package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"wzap/internal/message"
)

// NumberResolver is the number resolution contract consumed by the handlers.
type NumberResolver interface {
	Resolve(ctx context.Context, instanceID uuid.UUID, phone string) (jid string, err error)
}

// The resolver satisfies the handler contract; the assertion catches signature
// drift at build time.
var _ NumberResolver = (*message.JIDResolver)(nil)

// numberCheckRequest is the POST /instances/{id}/numbers/check payload.
type numberCheckRequest struct {
	Phone string `json:"phone"`
}

// numberCheckResponse is the JSON result of a number check. A phone that
// already contains '@' counts as resolved because the JID is returned as-is.
type numberCheckResponse struct {
	Exists     bool   `json:"exists"`
	JID        string `json:"jid"`
	Normalized string `json:"normalized"`
}

// handleCheckNumber answers 200 with the resolution of a phone number: exists
// is false and jid empty when the number is malformed or absent from WhatsApp.
// An instance without a usable session answers 503 so a caller can retry
// instead of taking a negative answer that was never verified.
//
// @Summary Check a phone number
// @Tags numbers
// @Accept json
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Param request body numberCheckRequest true "Phone payload"
// @Success 200 {object} numberCheckResponse "Resolution, wrapped in the data envelope"
// @Failure 400 {object} errorEnvelope "Malformed body or missing phone"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 413 {object} errorEnvelope "Body exceeds the 1 MiB limit"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Failure 503 {object} errorEnvelope "Number resolution unavailable"
// @Router /instances/{id}/numbers/check [post]
func handleCheckNumber(instances InstanceService, numbers NumberResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}

		var request numberCheckRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}
		phone := strings.TrimSpace(request.Phone)
		if phone == "" {
			Error(w, r, http.StatusBadRequest, "invalid_request", "phone is required")
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}

		// The target loads first (404 above); the ownership check denies with
		// 403 before the number is resolved.
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}

		normalized := message.NormalizePhone(phone)
		jid, err := numbers.Resolve(r.Context(), id, phone)
		switch {
		case err == nil:
			JSON(w, r, http.StatusOK, numberCheckResponse{Exists: true, JID: jid, Normalized: normalized})
		case errors.Is(err, message.ErrNumberNotFound):
			JSON(w, r, http.StatusOK, numberCheckResponse{Exists: false, Normalized: normalized})
		case errors.Is(err, message.ErrResolverUnavailable):
			Error(w, r, http.StatusServiceUnavailable, "unavailable", "number resolution unavailable")
		default:
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
		}
	}
}
