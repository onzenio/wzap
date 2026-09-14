package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/instance"
	"wzap/internal/model"
	"wzap/internal/session"
)

// connectPayload is the decoded data of a connect or qr response.
type connectPayload struct {
	Status      string     `json:"status"`
	QRCode      string     `json:"qr_code"`
	QRExpiresAt *time.Time `json:"qr_expires_at"`
}

func TestInstancesConnectStartsPairing(t *testing.T) {
	id := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	svc := &fakeInstanceService{connectFn: func(_ context.Context, gotID uuid.UUID) (instance.ConnectResult, error) {
		if gotID != id {
			t.Errorf("Connect id = %s, want %s", gotID, id)
		}
		return instance.ConnectResult{Status: session.StatusPairing, QRCode: "qr-123", QRExpiresAt: &expiresAt}, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+id.String()+"/connect", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data connectPayload `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Status != string(session.StatusPairing) {
		t.Errorf("data.status = %q, want %q", payload.Data.Status, session.StatusPairing)
	}
	if payload.Data.QRCode != "qr-123" {
		t.Errorf("data.qr_code = %q, want %q", payload.Data.QRCode, "qr-123")
	}
	if payload.Data.QRExpiresAt == nil || !payload.Data.QRExpiresAt.Equal(expiresAt) {
		t.Errorf("data.qr_expires_at = %v, want %v", payload.Data.QRExpiresAt, expiresAt)
	}
	if len(svc.connectIDs) != 1 || svc.connectIDs[0] != id {
		t.Errorf("Connect calls = %v, want [%s]", svc.connectIDs, id)
	}
}

func TestInstancesConnectAlreadyConnected(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{connectFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{Status: session.StatusConnected}, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+id.String()+"/connect", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data connectPayload `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Status != string(session.StatusConnected) {
		t.Errorf("data.status = %q, want %q", payload.Data.Status, session.StatusConnected)
	}
	if payload.Data.QRCode != "" {
		t.Errorf("data.qr_code = %q, want empty", payload.Data.QRCode)
	}
	if payload.Data.QRExpiresAt != nil {
		t.Errorf("data.qr_expires_at = %v, want null", payload.Data.QRExpiresAt)
	}
}

func TestInstancesConnectNotFound(t *testing.T) {
	svc := &fakeInstanceService{connectFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{}, instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/connect", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesQR(t *testing.T) {
	id := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	svc := &fakeInstanceService{qrFn: func(_ context.Context, gotID uuid.UUID) (instance.ConnectResult, error) {
		if gotID != id {
			t.Errorf("QR id = %s, want %s", gotID, id)
		}
		return instance.ConnectResult{Status: session.StatusPairing, QRCode: "qr-456", QRExpiresAt: &expiresAt}, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+id.String()+"/qr", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data connectPayload `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.QRCode != "qr-456" {
		t.Errorf("data.qr_code = %q, want %q", payload.Data.QRCode, "qr-456")
	}
	if payload.Data.QRExpiresAt == nil || !payload.Data.QRExpiresAt.Equal(expiresAt) {
		t.Errorf("data.qr_expires_at = %v, want %v", payload.Data.QRExpiresAt, expiresAt)
	}
	if len(svc.qrIDs) != 1 || svc.qrIDs[0] != id {
		t.Errorf("QR calls = %v, want [%s]", svc.qrIDs, id)
	}
}

func TestInstancesQRAlreadyConnected(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{qrFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{}, instance.ErrAlreadyConnected
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+id.String()+"/qr", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
		t.Errorf("error code = %q, want %q", code, "conflict")
	}
}

func TestInstancesStatus(t *testing.T) {
	connectedAt := time.Now().UTC().Truncate(time.Second)
	want := &model.Instance{
		ID: uuid.New(), Name: "loja", Status: string(session.StatusConnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net", LastError: "",
		LastConnectedAt: &connectedAt,
	}
	svc := &fakeInstanceService{getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
		if id != want.ID {
			t.Errorf("Get id = %s, want %s", id, want.ID)
		}
		return want, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+want.ID.String()+"/status", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data struct {
			Status          string     `json:"status"`
			WhatsAppJID     string     `json:"whatsapp_jid"`
			LastError       string     `json:"last_error"`
			LastConnectedAt *time.Time `json:"last_connected_at"`
		} `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Status != string(session.StatusConnected) {
		t.Errorf("data.status = %q, want %q", payload.Data.Status, session.StatusConnected)
	}
	if payload.Data.WhatsAppJID != want.WhatsAppJID {
		t.Errorf("data.whatsapp_jid = %q, want %q", payload.Data.WhatsAppJID, want.WhatsAppJID)
	}
	if payload.Data.LastConnectedAt == nil || !payload.Data.LastConnectedAt.Equal(connectedAt) {
		t.Errorf("data.last_connected_at = %v, want %v", payload.Data.LastConnectedAt, connectedAt)
	}
}

func TestInstancesStatusNotFound(t *testing.T) {
	svc := &fakeInstanceService{getFn: func(context.Context, uuid.UUID) (*model.Instance, error) {
		return nil, instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+uuid.NewString()+"/status", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesConnectionRejectsMalformedID(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "connect", method: http.MethodPost, path: "/instances/not-a-uuid/connect"},
		{name: "qr", method: http.MethodGet, path: "/instances/not-a-uuid/qr"},
		{name: "status", method: http.MethodGet, path: "/instances/not-a-uuid/status"},
		{name: "disconnect", method: http.MethodPost, path: "/instances/not-a-uuid/disconnect"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeInstanceService{}

			rec := serveJSON(t, instancesServer(t, svc), tt.method, tt.path, "")

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
				t.Errorf("error code = %q, want %q", code, "not_found")
			}
			if len(svc.connectIDs) != 0 || len(svc.qrIDs) != 0 || len(svc.getIDs) != 0 || len(svc.disconnectIDs) != 0 {
				t.Errorf("service called with a malformed id: connect=%v qr=%v get=%v disconnect=%v",
					svc.connectIDs, svc.qrIDs, svc.getIDs, svc.disconnectIDs)
			}
		})
	}
}

func TestInstancesDisconnect(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{disconnectFn: func(_ context.Context, gotID uuid.UUID) error {
		if gotID != id {
			t.Errorf("Disconnect id = %s, want %s", gotID, id)
		}
		return nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+id.String()+"/disconnect", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want empty", body)
	}
	if len(svc.disconnectIDs) != 1 || svc.disconnectIDs[0] != id {
		t.Errorf("Disconnect calls = %v, want [%s]", svc.disconnectIDs, id)
	}
}

func TestInstancesDisconnectNotFound(t *testing.T) {
	svc := &fakeInstanceService{disconnectFn: func(context.Context, uuid.UUID) error {
		return instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/disconnect", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesDisconnectFailure(t *testing.T) {
	svc := &fakeInstanceService{disconnectFn: func(context.Context, uuid.UUID) error {
		return errors.New("session still connected")
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/disconnect", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "internal_error" {
		t.Errorf("error code = %q, want %q", code, "internal_error")
	}
}
