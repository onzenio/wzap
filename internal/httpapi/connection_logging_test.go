package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/instance"
	"wzap/internal/model"
	"wzap/internal/session"
)

// capturedRecord is one slog record observed by memoryHandler.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// memoryHandler is an in-memory slog.Handler that records every log record so
// tests can assert which boundary lines a connection handler emits.
type memoryHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func newMemoryHandler() *memoryHandler { return &memoryHandler{} }

func (h *memoryHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *memoryHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *memoryHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *memoryHandler) WithGroup(string) slog.Handler { return h }

// snapshot returns a copy of the records captured so far.
func (h *memoryHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedRecord{}, h.records...)
}

// captureBoundaryLogs swaps the default logger for an in-memory handler and
// returns it, restoring the previous default when the test ends.
func captureBoundaryLogs(t *testing.T) *memoryHandler {
	t.Helper()
	h := newMemoryHandler()
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func findRecord(records []capturedRecord, msg string) (capturedRecord, bool) {
	for _, r := range records {
		if r.msg == msg {
			return r, true
		}
	}
	return capturedRecord{}, false
}

// assertNoQRBytes fails when any captured record carries the QR payload: only
// presence and expiry may be logged, never the code bytes.
func assertNoQRBytes(t *testing.T, h *memoryHandler, qr string) {
	t.Helper()
	if qr == "" {
		return
	}
	for _, r := range h.snapshot() {
		for key, value := range r.attrs {
			if s, ok := value.(string); ok && s == qr {
				t.Errorf("log record %q carries QR bytes in attr %q", r.msg, key)
			}
		}
		if r.msg == qr {
			t.Errorf("log message carries QR bytes: %q", r.msg)
		}
	}
}

func TestConnectBoundaryLogs(t *testing.T) {
	id := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	svc := &fakeInstanceService{connectFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{Status: session.StatusPairing, QRCode: "qr-123", QRExpiresAt: &expiresAt}, nil
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+id.String()+"/connect", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	records := h.snapshot()
	entry, ok := findRecord(records, "connect instance request")
	if !ok {
		t.Fatal("missing Debug record \"connect instance request\"")
	}
	if entry.level != slog.LevelDebug {
		t.Errorf("entry level = %v, want Debug", entry.level)
	}
	if entry.attrs["instance_id"] != id {
		t.Errorf("entry instance_id = %v, want %s", entry.attrs["instance_id"], id)
	}
	if entry.attrs["op"] != "connect" {
		t.Errorf("entry op = %v, want connect", entry.attrs["op"])
	}

	result, ok := findRecord(records, "connect instance result")
	if !ok {
		t.Fatal("missing Debug record \"connect instance result\"")
	}
	if result.level != slog.LevelDebug {
		t.Errorf("result level = %v, want Debug", result.level)
	}
	if result.attrs["status"] != string(session.StatusPairing) {
		t.Errorf("result status = %v, want %q", result.attrs["status"], session.StatusPairing)
	}
	if result.attrs["qr_present"] != true {
		t.Errorf("result qr_present = %v, want true", result.attrs["qr_present"])
	}
	if _, ok := result.attrs["expires_at"]; !ok {
		t.Error("result misses expires_at, want the QR validity")
	}
	assertNoQRBytes(t, h, "qr-123")
}

func TestConnectBoundaryLogsServiceError(t *testing.T) {
	svc := &fakeInstanceService{connectFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{}, errors.New("session dial failed")
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/connect", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	warn, ok := findRecord(h.snapshot(), "connect instance failed")
	if !ok {
		t.Fatal("missing Warn record \"connect instance failed\"")
	}
	if warn.level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", warn.level)
	}
	if warn.attrs["op"] != "connect" {
		t.Errorf("op = %v, want connect", warn.attrs["op"])
	}
	if _, ok := warn.attrs["error"]; !ok {
		t.Error("record misses error, want the service failure")
	}
}

func TestQRBoundaryLogs(t *testing.T) {
	id := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	svc := &fakeInstanceService{qrFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{Status: session.StatusPairing, QRCode: "qr-456", QRExpiresAt: &expiresAt}, nil
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+id.String()+"/qr", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	records := h.snapshot()
	if _, ok := findRecord(records, "qr instance request"); !ok {
		t.Error("missing Debug record \"qr instance request\"")
	}
	result, ok := findRecord(records, "qr instance result")
	if !ok {
		t.Fatal("missing Debug record \"qr instance result\"")
	}
	if result.attrs["qr_present"] != true {
		t.Errorf("result qr_present = %v, want true", result.attrs["qr_present"])
	}
	if _, ok := result.attrs["expires_at"]; !ok {
		t.Error("result misses expires_at, want the QR validity")
	}
	assertNoQRBytes(t, h, "qr-456")
}

func TestQRBoundaryLogsAlreadyConnected(t *testing.T) {
	svc := &fakeInstanceService{qrFn: func(context.Context, uuid.UUID) (instance.ConnectResult, error) {
		return instance.ConnectResult{}, instance.ErrAlreadyConnected
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+uuid.NewString()+"/qr", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}

	if _, ok := findRecord(h.snapshot(), "qr instance failed"); !ok {
		t.Error("missing Warn record \"qr instance failed\"")
	}
}

func TestStatusBoundaryLogs(t *testing.T) {
	wantID := uuid.New()
	svc := &fakeInstanceService{getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
		return &model.Instance{ID: id, Name: "loja", Status: string(session.StatusConnected)}, nil
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/instances/"+wantID.String()+"/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	records := h.snapshot()
	if _, ok := findRecord(records, "instance status request"); !ok {
		t.Error("missing Debug record \"instance status request\"")
	}
	result, ok := findRecord(records, "instance status result")
	if !ok {
		t.Fatal("missing Debug record \"instance status result\"")
	}
	if result.attrs["status"] != string(session.StatusConnected) {
		t.Errorf("result status = %v, want %q", result.attrs["status"], session.StatusConnected)
	}
}

func TestDisconnectBoundaryLogs(t *testing.T) {
	svc := &fakeInstanceService{}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/disconnect", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	records := h.snapshot()
	if _, ok := findRecord(records, "disconnect instance request"); !ok {
		t.Error("missing Debug record \"disconnect instance request\"")
	}
	if _, ok := findRecord(records, "disconnect instance result"); !ok {
		t.Error("missing Debug record \"disconnect instance result\"")
	}
}

func TestDisconnectBoundaryLogsServiceError(t *testing.T) {
	svc := &fakeInstanceService{disconnectFn: func(context.Context, uuid.UUID) error {
		return errors.New("session still connected")
	}}
	h := captureBoundaryLogs(t)

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances/"+uuid.NewString()+"/disconnect", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	warn, ok := findRecord(h.snapshot(), "disconnect instance failed")
	if !ok {
		t.Fatal("missing Warn record \"disconnect instance failed\"")
	}
	if warn.level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", warn.level)
	}
	if warn.attrs["op"] != "disconnect" {
		t.Errorf("op = %v, want disconnect", warn.attrs["op"])
	}
}
