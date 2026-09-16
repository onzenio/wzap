package whatsmeow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"wzap/internal/session"
)

// capturedRecord is one slog record observed by memoryHandler.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// memoryHandler is an in-memory slog.Handler that records every log record so
// tests can assert which boundary lines a pairing path emits.
type memoryHandler struct {
	min  slog.Level
	pre  []slog.Attr
	core *memoryCore
}

type memoryCore struct {
	mu      sync.Mutex
	records []capturedRecord
}

func newMemoryHandler(min slog.Level) *memoryHandler {
	return &memoryHandler{min: min, core: &memoryCore{}}
}

func (h *memoryHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.min
}

func (h *memoryHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, len(h.pre)+r.NumAttrs())
	for _, a := range h.pre {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.core.mu.Lock()
	h.core.records = append(h.core.records, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	h.core.mu.Unlock()
	return nil
}

func (h *memoryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &memoryHandler{
		min:  h.min,
		pre:  append(append([]slog.Attr{}, h.pre...), attrs...),
		core: h.core,
	}
}

func (h *memoryHandler) WithGroup(string) slog.Handler { return h }

// snapshot returns a copy of the records captured so far.
func (h *memoryHandler) snapshot() []capturedRecord {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	return append([]capturedRecord{}, h.core.records...)
}

func findRecords(records []capturedRecord, msg string) []capturedRecord {
	var out []capturedRecord
	for _, r := range records {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func waitForRecord(t *testing.T, h *memoryHandler, msg string) capturedRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range h.snapshot() {
			if r.msg == msg {
				return r
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for log record %q", msg)
	return capturedRecord{}
}

// assertNoSecret scans every captured record for value, failing when a log
// line carries secret material such as QR code bytes.
func assertNoSecret(t *testing.T, h *memoryHandler, value string) {
	t.Helper()
	for _, r := range h.snapshot() {
		if strings.Contains(r.msg, value) {
			t.Fatalf("log message %q contains secret value", r.msg)
		}
		for k, v := range r.attrs {
			if strings.Contains(fmt.Sprintf("%v", v), value) {
				t.Fatalf("log record %q attr %q contains secret value", r.msg, k)
			}
		}
	}
}

func newLoggedSession(t *testing.T, device *store.Device, h *memoryHandler, sink session.EventSink) *instanceSession {
	t.Helper()
	sess, err := newSession(uuid.New(), device, slog.New(h), sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	return sess
}

// TestMonitorQRLogsSuccess verifies the success terminal emits its boundary
// log while the session still reaches connected with the paired JID.
func TestMonitorQRLogsSuccess(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := newLoggedSession(t, &store.Device{ID: &jid}, h, sink)

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	pairing <- qrCodeItem("QR-SUCCESS-LOG")
	pairing <- whatsmeow.QRChannelSuccess

	rec := waitForRecord(t, h, "pairing succeeded")
	if rec.level != slog.LevelInfo {
		t.Errorf("pairing succeeded level = %v, want info", rec.level)
	}
	if rec.attrs["instance_id"] != sess.instanceID {
		t.Errorf("pairing succeeded instance_id = %v, want %v", rec.attrs["instance_id"], sess.instanceID)
	}
	assertNoSecret(t, h, "QR-SUCCESS-LOG")

	waitForPairing(t, "pairing success event", func() bool { return sink.count() > 0 })
	if sess.Status() != session.StatusConnected {
		t.Fatalf("session status = %q, want connected", sess.Status())
	}
}

// TestMonitorQRLogsTimeout verifies the QR expiry terminal emits its boundary
// log with the stable reason while the session still disconnects.
func TestMonitorQRLogsTimeout(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	sink := &recordingSink{}
	sess := newLoggedSession(t, &store.Device{}, h, sink)

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	pairing <- qrCodeItem("QR-TIMEOUT-LOG")
	pairing <- whatsmeow.QRChannelTimeout

	rec := waitForRecord(t, h, "pairing timed out")
	if rec.level != slog.LevelWarn {
		t.Errorf("pairing timed out level = %v, want warn", rec.level)
	}
	if rec.attrs["reason"] != "qr code expired" {
		t.Errorf("pairing timed out reason = %v, want %q", rec.attrs["reason"], "qr code expired")
	}
	assertNoSecret(t, h, "QR-TIMEOUT-LOG")

	waitForPairing(t, "expiry disconnect", func() bool {
		return sess.Status() == session.StatusDisconnected
	})
}

// TestMonitorQRLogsError verifies the error terminal emits its boundary log
// and the error-status transition additionally warns, without changing the
// resulting error state.
func TestMonitorQRLogsError(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	sink := &recordingSink{}
	sess := newLoggedSession(t, &store.Device{}, h, sink)

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	pairing <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: fmt.Errorf("boom")}

	rec := waitForRecord(t, h, "pairing failed")
	if rec.level != slog.LevelWarn {
		t.Errorf("pairing failed level = %v, want warn", rec.level)
	}
	if rec.attrs["instance_id"] != sess.instanceID {
		t.Errorf("pairing failed instance_id = %v, want %v", rec.attrs["instance_id"], sess.instanceID)
	}
	warn := waitForRecord(t, h, "session entered error status")
	if warn.level != slog.LevelWarn {
		t.Errorf("session entered error status level = %v, want warn", warn.level)
	}

	waitForPairing(t, "error status", func() bool {
		return sess.Status() == session.StatusError
	})
}

// TestMonitorQRLogsChannelClosed verifies an abruptly closed QR channel emits
// its boundary log while the session still disconnects.
func TestMonitorQRLogsChannelClosed(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	sess := newLoggedSession(t, &store.Device{}, h, nil)

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	close(pairing)

	rec := waitForRecord(t, h, "pairing qr channel closed")
	if rec.level != slog.LevelWarn {
		t.Errorf("pairing qr channel closed level = %v, want warn", rec.level)
	}
	if rec.attrs["reason"] != "qr channel closed" {
		t.Errorf("pairing qr channel closed reason = %v, want %q", rec.attrs["reason"], "qr channel closed")
	}
	waitForPairing(t, "closed-channel disconnect", func() bool {
		return sess.Status() == session.StatusDisconnected
	})
}

// TestQRCodeRotationLogsExpiryWithoutBytes verifies a code rotation emits a
// debug line carrying only the expiry, never the code bytes.
func TestQRCodeRotationLogsExpiryWithoutBytes(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	sess := newLoggedSession(t, &store.Device{}, h, nil)

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	pairing <- qrCodeItem("ROTATION-SECRET-CODE")

	rec := waitForRecord(t, h, "qr code rotated")
	if rec.level != slog.LevelDebug {
		t.Errorf("qr code rotated level = %v, want debug", rec.level)
	}
	if _, ok := rec.attrs["expires_at"]; !ok {
		t.Errorf("qr code rotated record is missing expires_at: %v", rec.attrs)
	}
	assertNoSecret(t, h, "ROTATION-SECRET-CODE")
	close(pairing)
}

// TestSetStatusLogsTransition verifies every status transition emits a debug
// line with from/to, jid presence and reason, that repeats stay silent, and
// that the error status additionally warns.
func TestSetStatusLogsTransition(t *testing.T) {
	h := newMemoryHandler(slog.LevelDebug)
	sess := newLoggedSession(t, &store.Device{}, h, nil)

	sess.setStatus(session.StatusPairing, "", "")
	rec := waitForRecord(t, h, "session status changed")
	if rec.level != slog.LevelDebug {
		t.Errorf("session status changed level = %v, want debug", rec.level)
	}
	if rec.attrs["from"] != session.StatusDisconnected || rec.attrs["to"] != session.StatusPairing {
		t.Errorf("session status changed from/to = %v/%v, want disconnected/pairing",
			rec.attrs["from"], rec.attrs["to"])
	}
	if rec.attrs["jid_present"] != false {
		t.Errorf("session status changed jid_present = %v, want false", rec.attrs["jid_present"])
	}
	before := len(h.snapshot())
	sess.setStatus(session.StatusPairing, "", "")
	time.Sleep(50 * time.Millisecond)
	if got := len(h.snapshot()); got != before {
		t.Fatalf("repeated setStatus emitted %d extra records, want none", got-before)
	}

	sess.setStatus(session.StatusError, "", "boom")
	warn := waitForRecord(t, h, "session entered error status")
	if warn.attrs["reason"] != "boom" {
		t.Errorf("session entered error status reason = %v, want %q", warn.attrs["reason"], "boom")
	}
	if sess.Status() != session.StatusError {
		t.Fatalf("session status = %q, want error", sess.Status())
	}
}
