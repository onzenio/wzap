package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

// runtimeRepo is an in-memory storage.InstanceRepository for the runtime tests.
// Only SetConnectionState (and the Get behind it) is exercised by the runtime;
// the remaining methods are stubs that must never be reached.
type runtimeRepo struct {
	instances   map[uuid.UUID]model.Instance
	getErr      error
	updateErr   error
	connections []connectionUpdate
}

// connectionUpdate records one SetConnectionState invocation.
type connectionUpdate struct {
	id              uuid.UUID
	status          string
	jid             string
	lastError       string
	lastConnectedAt *time.Time
}

func newRuntimeRepo(instances ...model.Instance) *runtimeRepo {
	repo := &runtimeRepo{instances: make(map[uuid.UUID]model.Instance)}
	for _, instance := range instances {
		repo.instances[instance.ID] = instance
	}
	return repo
}

func (r *runtimeRepo) Create(context.Context, model.Instance) (*model.Instance, error) {
	return nil, errors.New("runtimeRepo.Create: unexpected call")
}

func (r *runtimeRepo) Get(_ context.Context, id uuid.UUID) (*model.Instance, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	instance, ok := r.instances[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	stored := instance
	return &stored, nil
}

func (r *runtimeRepo) GetByExternalRef(context.Context, string) (*model.Instance, error) {
	return nil, errors.New("runtimeRepo.GetByExternalRef: unexpected call")
}

func (r *runtimeRepo) List(context.Context, int, string) ([]model.Instance, string, error) {
	return nil, "", errors.New("runtimeRepo.List: unexpected call")
}

func (r *runtimeRepo) Update(context.Context, model.Instance) (*model.Instance, error) {
	return nil, errors.New("runtimeRepo.Update: unexpected call")
}

func (r *runtimeRepo) Delete(context.Context, uuid.UUID) error {
	return errors.New("runtimeRepo.Delete: unexpected call")
}

func (r *runtimeRepo) SetConnection(context.Context, uuid.UUID, string, string) error {
	return errors.New("runtimeRepo.SetConnection: unexpected call")
}

// SetConnectionState records the targeted update and applies it to the
// in-memory row, mirroring the repository semantics.
func (r *runtimeRepo) SetConnectionState(_ context.Context, id uuid.UUID, status, jid, lastError string, connectedAt *time.Time) error {
	r.connections = append(r.connections, connectionUpdate{
		id: id, status: status, jid: jid, lastError: lastError, lastConnectedAt: connectedAt,
	})
	if r.updateErr != nil {
		return r.updateErr
	}
	instance, ok := r.instances[id]
	if !ok {
		return storage.ErrNotFound
	}
	instance.Status = status
	if jid != "" {
		instance.WhatsAppJID = jid
	}
	instance.LastError = lastError
	if connectedAt != nil {
		instance.LastConnectedAt = connectedAt
	}
	r.instances[id] = instance
	return nil
}

// fakeWriter records the events written to the outbox.
type fakeWriter struct {
	subjects []string
	events   []events.Envelope
	writeErr error
}

func (w *fakeWriter) Write(_ context.Context, subject string, env events.Envelope) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	w.subjects = append(w.subjects, subject)
	w.events = append(w.events, env)
	return nil
}

// decodedConnectionPayload is the decoded connection event body.
type decodedConnectionPayload struct {
	Status      string `json:"status"`
	WhatsAppJID string `json:"whatsapp_jid"`
	Reason      string `json:"reason"`
}

func decodeConnectionPayload(t *testing.T, env events.Envelope) decodedConnectionPayload {
	t.Helper()
	var payload decodedConnectionPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode connection payload %q: %v", env.Payload, err)
	}
	return payload
}

func TestRuntimeOnConnectionUpdatesInstanceAndEnqueuesEvent(t *testing.T) {
	id := uuid.New()
	repo := newRuntimeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusDisconnected),
		WhatsAppJID: "5511@wa", LastError: "old failure",
	})
	writer := &fakeWriter{}
	runtime := NewRuntime(repo, writer, nil, nil, "", 0, nil)

	runtime.OnConnection(context.Background(), id, session.StatusConnected, "5511999999999@s.whatsapp.net", "")

	stored := repo.instances[id]
	if stored.Status != string(session.StatusConnected) {
		t.Errorf("stored status = %q, want %q", stored.Status, session.StatusConnected)
	}
	if stored.WhatsAppJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("stored whatsapp_jid = %q, want the connected JID", stored.WhatsAppJID)
	}
	if stored.LastError != "" {
		t.Errorf("stored last_error = %q, want empty after a clean connect", stored.LastError)
	}
	if stored.LastConnectedAt == nil {
		t.Error("stored last_connected_at = nil, want the connection time")
	}
	if len(repo.connections) != 1 {
		t.Fatalf("SetConnectionState calls = %+v, want one", repo.connections)
	}
	update := repo.connections[0]
	if update.id != id || update.status != string(session.StatusConnected) ||
		update.jid != "5511999999999@s.whatsapp.net" || update.lastError != "" {
		t.Errorf("SetConnectionState call = %+v, want the connected transition", update)
	}
	if update.lastConnectedAt == nil {
		t.Error("SetConnectionState call is missing last_connected_at")
	}

	if len(writer.subjects) != 1 {
		t.Fatalf("event writes = %v, want one event", writer.subjects)
	}
	if want := events.Subjects.Connection(id); writer.subjects[0] != want {
		t.Errorf("event subject = %q, want %q", writer.subjects[0], want)
	}
	env := writer.events[0]
	if env.Type != "connection" {
		t.Errorf("event type = %q, want %q", env.Type, "connection")
	}
	if env.InstanceID != id {
		t.Errorf("event instance = %s, want %s", env.InstanceID, id)
	}
	payload := decodeConnectionPayload(t, env)
	if payload.Status != string(session.StatusConnected) {
		t.Errorf("payload.status = %q, want %q", payload.Status, session.StatusConnected)
	}
	if payload.WhatsAppJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("payload.whatsapp_jid = %q, want the connected JID", payload.WhatsAppJID)
	}
}

func TestRuntimeOnConnectionFailureRecordsReason(t *testing.T) {
	id := uuid.New()
	connectedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repo := newRuntimeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusConnected),
		WhatsAppJID: "5511@wa", LastConnectedAt: &connectedAt,
	})
	writer := &fakeWriter{}
	runtime := NewRuntime(repo, writer, nil, nil, "", 0, nil)

	runtime.OnConnection(context.Background(), id, session.StatusError, "", "temporary ban")

	stored := repo.instances[id]
	if stored.Status != string(session.StatusError) {
		t.Errorf("stored status = %q, want %q", stored.Status, session.StatusError)
	}
	if stored.WhatsAppJID != "5511@wa" {
		t.Errorf("stored whatsapp_jid = %q, want the previous JID kept", stored.WhatsAppJID)
	}
	if stored.LastError != "temporary ban" {
		t.Errorf("stored last_error = %q, want %q", stored.LastError, "temporary ban")
	}
	if stored.LastConnectedAt == nil || !stored.LastConnectedAt.Equal(connectedAt) {
		t.Errorf("stored last_connected_at = %v, want the previous %v", stored.LastConnectedAt, connectedAt)
	}

	if len(repo.connections) != 1 {
		t.Fatalf("SetConnectionState calls = %+v, want one", repo.connections)
	}
	if update := repo.connections[0]; update.lastError != "temporary ban" || update.lastConnectedAt != nil {
		t.Errorf("SetConnectionState call = %+v, want the reason without a connection time", update)
	}

	payload := decodeConnectionPayload(t, writer.events[0])
	if payload.Status != string(session.StatusError) || payload.Reason != "temporary ban" {
		t.Errorf("payload = %+v, want status error with the reason", payload)
	}
}

func TestRuntimeOnConnectionUnknownInstanceSkipsEvent(t *testing.T) {
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "", 0, nil)

	runtime.OnConnection(context.Background(), uuid.New(), session.StatusConnected, "5511@wa", "")

	if len(writer.subjects) != 0 {
		t.Errorf("event writes = %v, want none for an unknown instance", writer.subjects)
	}
}

func TestRuntimeOnConnectionUpdateFailureSkipsEvent(t *testing.T) {
	id := uuid.New()
	repo := newRuntimeRepo(model.Instance{ID: id, Status: string(session.StatusDisconnected)})
	repo.updateErr = errors.New("database down")
	writer := &fakeWriter{}
	runtime := NewRuntime(repo, writer, nil, nil, "", 0, nil)

	runtime.OnConnection(context.Background(), id, session.StatusConnected, "5511@wa", "")

	if len(writer.subjects) != 0 {
		t.Errorf("event writes = %v, want none when the status update failed", writer.subjects)
	}
	if stored := repo.instances[id]; stored.Status != string(session.StatusDisconnected) {
		t.Errorf("stored status = %q, want the unchanged %q", stored.Status, session.StatusDisconnected)
	}
}

// fakeReceiptApplier records the receipts handed to the runtime.
type fakeReceiptApplier struct {
	applied []session.Receipt
	err     error
}

func (f *fakeReceiptApplier) Apply(_ context.Context, receipt session.Receipt) error {
	f.applied = append(f.applied, receipt)
	return f.err
}

func TestRuntimeOnReceiptAppliesReceipt(t *testing.T) {
	id := uuid.New()
	applier := &fakeReceiptApplier{}
	runtime := NewRuntime(newRuntimeRepo(), &fakeWriter{}, applier, nil, "", 0, nil)
	receipt := session.Receipt{
		InstanceID: id,
		MessageIDs: []string{"wamid.1"},
		Status:     "read",
	}

	runtime.OnReceipt(context.Background(), receipt)

	if len(applier.applied) != 1 {
		t.Fatalf("applied receipts = %d, want 1", len(applier.applied))
	}
	if applier.applied[0].InstanceID != id || applier.applied[0].MessageIDs[0] != "wamid.1" {
		t.Errorf("applied receipt = %+v, want the session receipt", applier.applied[0])
	}
}

func TestRuntimeOnReceiptFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	applier := &fakeReceiptApplier{err: errors.New("database down")}
	runtime := NewRuntime(newRuntimeRepo(), &fakeWriter{}, applier, nil, "", 0, slog.New(slog.NewTextHandler(&logs, nil)))

	runtime.OnReceipt(context.Background(), session.Receipt{InstanceID: uuid.New(), MessageIDs: []string{"wamid.1"}})

	if !strings.Contains(logs.String(), "apply receipt") || !strings.Contains(logs.String(), "database down") {
		t.Errorf("logs = %q, want the receipt failure", logs.String())
	}
}

func TestRuntimeOnReceiptWithoutApplierIsNoOp(t *testing.T) {
	runtime := NewRuntime(newRuntimeRepo(), &fakeWriter{}, nil, nil, "", 0, nil)

	runtime.OnReceipt(context.Background(), session.Receipt{InstanceID: uuid.New(), MessageIDs: []string{"wamid.1"}})
}
