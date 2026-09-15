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
// connected. It loads the target first (404) and authorizes (403) before
// touching the session.
//
// @Summary Connect an instance
// @Tags connection
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 200 {object} connectResponse "Pairing result, wrapped in the data envelope"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 409 {object} errorEnvelope "Instance already connected"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id}/connect [post]
func handleConnectInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}

		result, err := instances.Connect(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, r, http.StatusOK, newConnectResponse(result))
	}
}

// handleQRInstance answers 200 with the current pairing QR of an instance, or
// 409 when its session is already connected. It loads the target first (404)
// and authorizes (403) before touching the session.
//
// @Summary Get the pairing QR
// @Tags connection
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 200 {object} connectResponse "Current QR, wrapped in the data envelope"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 409 {object} errorEnvelope "Instance already connected"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id}/qr [get]
func handleQRInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}

		result, err := instances.QR(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, r, http.StatusOK, newConnectResponse(result))
	}
}

// handleInstanceStatus answers 200 with the connection status of an instance.
//
// @Summary Get connection status
// @Tags connection
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 200 {object} statusResponse "Connection status, wrapped in the data envelope"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id}/status [get]
func handleInstanceStatus(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}

		found, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, found); err != nil {
			writeForbidden(w, r)
			return
		}
		JSON(w, r, http.StatusOK, statusResponse{
			Status:          found.Status,
			WhatsAppJID:     found.WhatsAppJID,
			LastError:       found.LastError,
			LastConnectedAt: found.LastConnectedAt,
		})
	}
}

// handleDisconnectInstance ends the session of an instance, clearing its
// paired identity and connection state, and answers 204. It loads the target
// first (404) and authorizes (403) before touching the session.
//
// @Summary Disconnect an instance
// @Tags connection
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 204 "Disconnected, no body"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id}/disconnect [post]
func handleDisconnectInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}

		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}

		if err := instances.Disconnect(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, r, http.StatusNoContent, nil)
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
