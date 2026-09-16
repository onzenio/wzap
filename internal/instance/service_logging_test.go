package instance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
)

// serviceRecord is one slog record observed by serviceMemoryHandler.
type serviceRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// serviceMemoryHandler is an in-memory slog.Handler that records every log
// record so tests can assert which branch lines Connect/QR emit.
type serviceMemoryHandler struct {
	pre  []slog.Attr
	core *serviceMemoryCore
}

type serviceMemoryCore struct {
	mu      sync.Mutex
	records []serviceRecord
}

func newServiceMemoryHandler() *serviceMemoryHandler {
	return &serviceMemoryHandler{core: &serviceMemoryCore{}}
}

func (h *serviceMemoryHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *serviceMemoryHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, len(h.pre)+r.NumAttrs())
	for _, a := range h.pre {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.core.mu.Lock()
	h.core.records = append(h.core.records, serviceRecord{level: r.Level, msg: r.Message, attrs: attrs})
	h.core.mu.Unlock()
	return nil
}

func (h *serviceMemoryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &serviceMemoryHandler{
		pre:  append(append([]slog.Attr{}, h.pre...), attrs...),
		core: h.core,
	}
}

func (h *serviceMemoryHandler) WithGroup(string) slog.Handler { return h }

// snapshot returns a copy of the records captured so far.
func (h *serviceMemoryHandler) snapshot() []serviceRecord {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	return append([]serviceRecord{}, h.core.records...)
}

// captureServiceLogs swaps the default logger for an in-memory handler and
// returns it, restoring the previous default when the test ends.
func captureServiceLogs(t *testing.T) *serviceMemoryHandler {
	t.Helper()
	h := newServiceMemoryHandler()
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func findServiceRecord(records []serviceRecord, msg string, branch string) (serviceRecord, bool) {
	for _, r := range records {
		if r.msg == msg && r.attrs["branch"] == branch {
			return r, true
		}
	}
	return serviceRecord{}, false
}

// assertNoServiceSecret fails when any captured record carries QR code bytes:
// only presence and expiry may be logged, never the code itself.
func assertNoServiceSecret(t *testing.T, h *serviceMemoryHandler, qr string) {
	t.Helper()
	if qr == "" {
		return
	}
	for _, r := range h.snapshot() {
		if strings.Contains(r.msg, qr) {
			t.Fatalf("log message %q contains secret value", r.msg)
		}
		for k, v := range r.attrs {
			if strings.Contains(fmt.Sprintf("%v", v), qr) {
				t.Fatalf("log record %q attr %q contains secret value", r.msg, k)
			}
		}
	}
}

// storedCredsSession is a fake session whose Connect reports stored
// credentials: no QR code and no error, like a device pairing without a scan.
type storedCredsSession struct {
	*sessiontest.FakeSession
}

// Connect answers stored credentials without a QR code.
func (s *storedCredsSession) Connect(context.Context) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// storedCredsManager returns a registered session whose device holds stored
// credentials, as the manager does for a paired instance.
type storedCredsManager struct {
	*sessiontest.Fake
	sess *storedCredsSession
}

// Create returns the stored-credentials session.
func (m *storedCredsManager) Create(*model.Instance) (session.Session, error) {
	return m.sess, nil
}

func TestConnectLogsAlreadyConnectedBranch(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusConnected)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusConnected)
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if result.Status != session.StatusConnected {
		t.Fatalf("Status = %q, want %q", result.Status, session.StatusConnected)
	}

	rec, ok := findServiceRecord(h.snapshot(), "connect instance branch", "already-connected")
	if !ok {
		t.Fatal("missing Debug record \"connect instance branch\" with branch already-connected")
	}
	if rec.level != slog.LevelDebug {
		t.Errorf("level = %v, want Debug", rec.level)
	}
	if rec.attrs["op"] != "connect" {
		t.Errorf("op = %v, want connect", rec.attrs["op"])
	}
	if rec.attrs["instance_id"] != id {
		t.Errorf("instance_id = %v, want %s", rec.attrs["instance_id"], id)
	}
}

func TestConnectLogsAlreadyPairingBranch(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusPairing)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sessions.Put(id, sess)
	if _, _, err := sess.Connect(context.Background()); err != nil {
		t.Fatalf("setup session Connect: %v", err)
	}
	svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	rec, ok := findServiceRecord(h.snapshot(), "connect instance branch", "already-pairing")
	if !ok {
		t.Fatal("missing Debug record \"connect instance branch\" with branch already-pairing")
	}
	if rec.attrs["qr_present"] != true {
		t.Errorf("qr_present = %v, want true", rec.attrs["qr_present"])
	}
	if _, ok := rec.attrs["expires_at"]; !ok {
		t.Error("record misses expires_at, want the QR validity")
	}
	assertNoServiceSecret(t, h, result.QRCode)
}

func TestConnectLogsNewPairingBranch(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	rec, ok := findServiceRecord(h.snapshot(), "connect instance branch", "new-pairing")
	if !ok {
		t.Fatal("missing Debug record \"connect instance branch\" with branch new-pairing")
	}
	if rec.attrs["status"] != string(session.StatusPairing) {
		t.Errorf("status = %v, want %q", rec.attrs["status"], session.StatusPairing)
	}
	if rec.attrs["qr_present"] != true {
		t.Errorf("qr_present = %v, want true", rec.attrs["qr_present"])
	}
	if _, ok := rec.attrs["expires_at"]; !ok {
		t.Error("record misses expires_at, want the QR validity")
	}
	assertNoServiceSecret(t, h, result.QRCode)
}

func TestConnectLogsStoredCredentialsBranch(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	sess := &storedCredsSession{FakeSession: sessiontest.NewSession(id, nil)}
	sessions := &storedCredsManager{Fake: sessiontest.New(nil), sess: sess}
	svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if result.Status != session.StatusConnected {
		t.Fatalf("Status = %q, want %q", result.Status, session.StatusConnected)
	}

	if _, ok := findServiceRecord(h.snapshot(), "connect instance branch", "stored-credentials"); !ok {
		t.Error("missing Debug record \"connect instance branch\" with branch stored-credentials")
	}
}

func TestConnectLogsPairingError(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.ConnectErr = errors.New("dial failed")
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	if _, err := svc.Connect(context.Background(), id); err == nil {
		t.Fatal("Connect error = nil, want the session failure")
	}

	rec, ok := findServiceRecord(h.snapshot(), "connect instance failed", "new-pairing")
	if !ok {
		t.Fatal("missing Warn record \"connect instance failed\" with branch new-pairing")
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", rec.level)
	}
	if _, ok := rec.attrs["error"]; !ok {
		t.Error("record misses error, want the session failure")
	}
}

func TestConnectLogsStaleDeviceReset(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusDisconnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sess := &oneShotNoDeviceSession{FakeSession: sessiontest.NewSession(id, nil), fails: 1}
	sessions := &staleConnectManager{Fake: sessiontest.New(nil), sess: sess}
	svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if result.Status != session.StatusPairing {
		t.Fatalf("Status = %q, want %q after the reset", result.Status, session.StatusPairing)
	}

	if _, ok := findServiceRecord(h.snapshot(), "connect pairing device gone, resetting stale pairing", "reset-stale-device"); !ok {
		t.Error("missing Debug record for the ErrNoDevice reset path")
	}
	if _, ok := findServiceRecord(h.snapshot(), "reset stale pairing", "reset-stale-device"); !ok {
		t.Error("missing Debug record \"reset stale pairing\"")
	}
	if _, ok := findServiceRecord(h.snapshot(), "connect instance branch", "new-pairing"); !ok {
		t.Error("missing Debug record \"connect instance branch\" with branch new-pairing after the reset")
	}
	assertNoServiceSecret(t, h, result.QRCode)
}

func TestConnectLogsPersistPairingError(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	repo.setConnectionErr = errors.New("database down")
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)
	h := captureServiceLogs(t)

	if _, err := svc.Connect(context.Background(), id); err == nil {
		t.Fatal("Connect error = nil, want the pairing update failure")
	}

	rec, ok := findServiceRecord(h.snapshot(), "connect instance failed", "persist-pairing")
	if !ok {
		t.Fatal("missing Warn record \"connect instance failed\" with branch persist-pairing")
	}
	if rec.level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", rec.level)
	}
}

func TestQRLogsBranches(t *testing.T) {
	t.Run("already connected", func(t *testing.T) {
		id := uuid.New()
		repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusConnected)})
		sessions := sessiontest.New(nil)
		sess := sessiontest.NewSession(id, nil)
		sess.SetStatus(session.StatusConnected)
		sessions.Put(id, sess)
		svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
		h := captureServiceLogs(t)

		if _, err := svc.QR(context.Background(), id); !errors.Is(err, ErrAlreadyConnected) {
			t.Fatalf("QR error = %v, want ErrAlreadyConnected", err)
		}
		if _, ok := findServiceRecord(h.snapshot(), "qr instance branch", "already-connected"); !ok {
			t.Error("missing Debug record \"qr instance branch\" with branch already-connected")
		}
	})

	t.Run("new pairing", func(t *testing.T) {
		id := uuid.New()
		repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
		svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)
		h := captureServiceLogs(t)

		result, err := svc.QR(context.Background(), id)
		if err != nil {
			t.Fatalf("QR: %v", err)
		}
		rec, ok := findServiceRecord(h.snapshot(), "qr instance branch", "new-pairing")
		if !ok {
			t.Fatal("missing Debug record \"qr instance branch\" with branch new-pairing")
		}
		if rec.attrs["qr_present"] != true {
			t.Errorf("qr_present = %v, want true", rec.attrs["qr_present"])
		}
		assertNoServiceSecret(t, h, result.QRCode)
	})

	t.Run("stored credentials", func(t *testing.T) {
		id := uuid.New()
		repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
		sess := &storedCredsSession{FakeSession: sessiontest.NewSession(id, nil)}
		sessions := &storedCredsManager{Fake: sessiontest.New(nil), sess: sess}
		svc := NewService(repo, sessions, &fakeMedia{}, nil, nil)
		h := captureServiceLogs(t)

		if _, err := svc.QR(context.Background(), id); !errors.Is(err, ErrAlreadyConnected) {
			t.Fatalf("QR error = %v, want ErrAlreadyConnected", err)
		}
		if _, ok := findServiceRecord(h.snapshot(), "qr instance branch", "stored-credentials"); !ok {
			t.Error("missing Debug record \"qr instance branch\" with branch stored-credentials")
		}
	})
}
