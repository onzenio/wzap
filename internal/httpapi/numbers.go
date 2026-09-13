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
func handleCheckNumber(instances InstanceService, numbers NumberResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
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

		if _, err := instances.Get(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}

		normalized := message.NormalizePhone(phone)
		jid, err := numbers.Resolve(r.Context(), id, phone)
		switch {
		case err == nil:
			JSON(w, http.StatusOK, numberCheckResponse{Exists: true, JID: jid, Normalized: normalized})
		case errors.Is(err, message.ErrNumberNotFound):
			JSON(w, http.StatusOK, numberCheckResponse{Exists: false, Normalized: normalized})
		case errors.Is(err, message.ErrResolverUnavailable):
			Error(w, r, http.StatusServiceUnavailable, "unavailable", "number resolution unavailable")
		default:
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
		}
	}
}
