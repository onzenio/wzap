package httpapi

import (
	"net/http"
	"time"

	"wzap/internal/instance"
)

// connectResponse is the JSON representation of a pairing result.
type connectResponse struct {
	Status      string     `json:"status"`
	QRCode      string     `json:"qr_code,omitempty"`
	QRExpiresAt *time.Time `json:"qr_expires_at,omitempty"`
}

// statusResponse is the JSON representation of the connection status of an
// instance.
type statusResponse struct {
	Status          string     `json:"status"`
	WhatsAppJID     string     `json:"whatsapp_jid"`
	LastError       string     `json:"last_error"`
	LastConnectedAt *time.Time `json:"last_connected_at"`
}

// handleConnectInstance starts pairing and answers 200 with the QR code and its
// validity, or with the status and no QR when the instance is already
// connected.
func handleConnectInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		result, err := instances.Connect(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, newConnectResponse(result))
	}
}

// handleQRInstance answers 200 with the current pairing QR of an instance, or
// 409 when its session is already connected.
func handleQRInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		result, err := instances.QR(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, newConnectResponse(result))
	}
}

// handleInstanceStatus answers 200 with the connection status of an instance.
func handleInstanceStatus(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		found, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, statusResponse{
			Status:          found.Status,
			WhatsAppJID:     found.WhatsAppJID,
			LastError:       found.LastError,
			LastConnectedAt: found.LastConnectedAt,
		})
	}
}

// handleDisconnectInstance ends the session of an instance, clearing its
// paired identity and connection state, and answers 204.
func handleDisconnectInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		if err := instances.Disconnect(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusNoContent, nil)
	}
}

// newConnectResponse maps a pairing result to its JSON representation.
func newConnectResponse(result instance.ConnectResult) connectResponse {
	return connectResponse{
		Status:      string(result.Status),
		QRCode:      result.QRCode,
		QRExpiresAt: result.QRExpiresAt,
	}
}
