package instance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
	"wzap/internal/storage"
)

var (
	_ storage.InstanceRepository = (*fakeRepo)(nil)
	_ MediaRemover               = (*fakeMedia)(nil)
	_ session.Manager            = (*recordingManager)(nil)
	_ session.Manager            = (*stalePairingManager)(nil)
	_ session.Manager            = (*staleConnectManager)(nil)
	_ session.Session            = (*oneShotNoDeviceSession)(nil)
)

// fakeRepo is an in-memory storage.InstanceRepository for the service tests. It
// records the calls and lets tests force storage failures.
type fakeRepo struct {
	instances map[uuid.UUID]model.Instance

	createErr        error
	updateErr        error
	setConnectionErr error
	deleteErr        error
	listErr          error

	listResult []model.Instance
	nextCursor string
	listLimit  int
	listCursor string

	createCalls        []model.Instance
	updateCalls        []model.Instance
	setConnectionCalls []setConnectionCall
	deleteCalls        []uuid.UUID

	order *[]string
}

// setConnectionCall is one recorded SetConnection invocation.
type setConnectionCall struct {
	id     uuid.UUID
	status string
	jid    string
}

func newFakeRepo(instances ...model.Instance) *fakeRepo {
	repo := &fakeRepo{instances: make(map[uuid.UUID]model.Instance)}
	for _, instance := range instances {
		repo.instances[instance.ID] = instance
	}
	return repo
}

// Create stores instance and returns it, or the forced error when set.
func (r *fakeRepo) Create(_ context.Context, instance model.Instance) (*model.Instance, error) {
	r.createCalls = append(r.createCalls, instance)
	if r.createErr != nil {
		return nil, r.createErr
	}
	r.instances[instance.ID] = instance
	stored := instance
	return &stored, nil
}

// Get returns the stored instance or storage.ErrNotFound.
func (r *fakeRepo) Get(_ context.Context, id uuid.UUID) (*model.Instance, error) {
	instance, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("get instance: %w", storage.ErrNotFound)
	}
	stored := instance
	return &stored, nil
}

// GetByExternalRef returns the stored instance with externalRef or
// storage.ErrNotFound.
func (r *fakeRepo) GetByExternalRef(_ context.Context, externalRef string) (*model.Instance, error) {
	for _, instance := range r.instances {
		if instance.ExternalRef == externalRef {
			stored := instance
			return &stored, nil
		}
	}
	return nil, fmt.Errorf("get instance by external ref: %w", storage.ErrNotFound)
}

// List returns the configured page and records the pagination arguments, or the
// forced error when set.
func (r *fakeRepo) List(_ context.Context, limit int, cursor string) ([]model.Instance, string, error) {
	r.listLimit = limit
	r.listCursor = cursor
	if r.listErr != nil {
		return nil, "", r.listErr
	}
	return r.listResult, r.nextCursor, nil
}

// Update stores instance and returns it, or the forced error when set.
func (r *fakeRepo) Update(_ context.Context, instance model.Instance) (*model.Instance, error) {
	r.updateCalls = append(r.updateCalls, instance)
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	r.instances[instance.ID] = instance
	stored := instance
	return &stored, nil
}

// SetConnection applies the partial update, or returns the forced error when
// set.
func (r *fakeRepo) SetConnection(_ context.Context, id uuid.UUID, status, jid string) error {
	r.setConnectionCalls = append(r.setConnectionCalls, setConnectionCall{id: id, status: status, jid: jid})
	if r.setConnectionErr != nil {
		return r.setConnectionErr
	}
	instance, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("set instance connection: %w", storage.ErrNotFound)
	}
	instance.Status = status
	instance.WhatsAppJID = jid
	r.instances[id] = instance
	return nil
}

// SetConnectionState is only exercised by the session runtime, not this service.
func (r *fakeRepo) SetConnectionState(context.Context, uuid.UUID, string, string, string, *time.Time) error {
	return errors.New("fakeRepo.SetConnectionState: unexpected call")
}

// Delete removes instance, or returns storage.ErrNotFound.
func (r *fakeRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.deleteCalls = append(r.deleteCalls, id)
	r.record("repo")
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if _, ok := r.instances[id]; !ok {
		return fmt.Errorf("delete instance: %w", storage.ErrNotFound)
	}
	delete(r.instances, id)
	return nil
}

func (r *fakeRepo) record(step string) {
	if r.order != nil {
		*r.order = append(*r.order, step)
	}
}

// fakeMedia is an in-memory MediaRemover that records its calls.
type fakeMedia struct {
	err   error
	order *[]string

	calls []uuid.UUID
}

// DeleteByInstance records the call, appends its step to the shared operation
// log and returns the forced error when set.
func (m *fakeMedia) DeleteByInstance(_ context.Context, instanceID uuid.UUID) error {
	m.calls = append(m.calls, instanceID)
	if m.order != nil {
		*m.order = append(*m.order, "media")
	}
	return m.err
}

// recordingManager wraps sessiontest.Fake to append the remove step to the
// shared operation log.
type recordingManager struct {
	*sessiontest.Fake
	order *[]string
}

// Remove records the removal step before delegating to the fake manager.
func (m *recordingManager) Remove(ctx context.Context, instanceID uuid.UUID) error {
	if m.order != nil {
		*m.order = append(*m.order, "session")
	}
	return m.Fake.Remove(ctx, instanceID)
}

// stalePairingManager fails its first Create with session.ErrNoDevice, like the
// manager does when the persisted device of an instance is gone.
type stalePairingManager struct {
	*sessiontest.Fake
	mu       sync.Mutex
	failures int
	attempts []model.Instance
}

// Create fails with ErrNoDevice while failures remain, recording every attempt.
func (m *stalePairingManager) Create(instance *model.Instance) (session.Session, error) {
	m.mu.Lock()
	m.attempts = append(m.attempts, *instance)
	fail := m.failures > 0
	if fail {
		m.failures--
	}
	m.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("create session: device %s: %w", instance.WhatsAppJID, session.ErrNoDevice)
	}
	return m.Fake.Create(instance)
}

// createAttempts returns the instances passed to Create, in order.
func (m *stalePairingManager) createAttempts() []model.Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Instance(nil), m.attempts...)
}

// oneShotNoDeviceSession is a FakeSession whose first Connect reports
// ErrNoDevice, simulating a device deleted by an external logout while the
// session is still registered.
type oneShotNoDeviceSession struct {
	*sessiontest.FakeSession
	mu    sync.Mutex
	fails int
}

// Connect fails with ErrNoDevice while fails remain, then delegates.
func (s *oneShotNoDeviceSession) Connect(ctx context.Context) (string, time.Time, error) {
	s.mu.Lock()
	fail := s.fails > 0
	if fail {
		s.fails--
	}
	s.mu.Unlock()
	if fail {
		return "", time.Time{}, fmt.Errorf("connect session: %w", session.ErrNoDevice)
	}
	return s.FakeSession.Connect(ctx)
}

// staleConnectManager returns a registered session whose device was deleted.
type staleConnectManager struct {
	*sessiontest.Fake
	sess *oneShotNoDeviceSession
}

// Create returns the stale session, as the manager does for a registered
// instance.
func (m *staleConnectManager) Create(*model.Instance) (session.Session, error) {
	return m.sess, nil
}

// strptr returns a pointer to s for the partial update inputs.
func strptr(s string) *string { return &s }

func TestServiceCreate(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	created, err := svc.Create(context.Background(), CreateInput{Name: "loja", ExternalRef: "crm-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.ID == uuid.Nil {
		t.Error("ID = nil, want a generated UUID")
	}
	if created.Status != string(session.StatusDisconnected) {
		t.Errorf("Status = %q, want %q", created.Status, session.StatusDisconnected)
	}
	if created.Name != "loja" || created.ExternalRef != "crm-1" {
		t.Errorf("created = %+v, want name loja and external ref crm-1", created)
	}
	if len(repo.createCalls) != 1 || repo.createCalls[0].ID != created.ID {
		t.Errorf("repo Create calls = %+v, want the generated instance", repo.createCalls)
	}
}

func TestServiceCreateExternalRefTaken(t *testing.T) {
	repo := newFakeRepo()
	repo.createErr = fmt.Errorf("insert instance: %w", storage.ErrExternalRefTaken)
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Create(context.Background(), CreateInput{Name: "loja", ExternalRef: "crm-1"})
	if !errors.Is(err, ErrExternalRefTaken) {
		t.Fatalf("Create error = %v, want ErrExternalRefTaken", err)
	}
}

func TestServiceGet(t *testing.T) {
	want := model.Instance{ID: uuid.New(), Name: "loja", Status: "connected"}
	svc := NewService(newFakeRepo(want), sessiontest.New(nil), &fakeMedia{})

	got, err := svc.Get(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("Get = %+v, want %+v", *got, want)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Get(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestServiceList(t *testing.T) {
	repo := newFakeRepo()
	repo.listResult = []model.Instance{{ID: uuid.New(), Name: "a"}}
	repo.nextCursor = "cursor-1"
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	items, next, err := svc.List(context.Background(), 25, "cursor-0")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Name != "a" {
		t.Errorf("List items = %+v, want the configured page", items)
	}
	if next != "cursor-1" {
		t.Errorf("next cursor = %q, want %q", next, "cursor-1")
	}
	if repo.listLimit != 25 || repo.listCursor != "cursor-0" {
		t.Errorf("repo List(%d, %q), want (25, cursor-0)", repo.listLimit, repo.listCursor)
	}
}

func TestServiceListInvalidCursor(t *testing.T) {
	repo := newFakeRepo()
	repo.listErr = fmt.Errorf("list instances: %w", storage.ErrInvalidCursor)
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	_, _, err := svc.List(context.Background(), 10, "not-a-uuid")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("List error = %v, want ErrInvalidCursor", err)
	}
}

func TestServiceUpdatePartial(t *testing.T) {
	tests := []struct {
		name  string
		input UpdateInput
		want  model.Instance
	}{
		{
			name:  "name only",
			input: UpdateInput{Name: strptr("novo")},
			want:  model.Instance{Name: "novo", ExternalRef: "ref-1", Status: "connected", WhatsAppJID: "5511@wa"},
		},
		{
			name:  "external ref only",
			input: UpdateInput{ExternalRef: strptr("ref-2")},
			want:  model.Instance{Name: "antigo", ExternalRef: "ref-2", Status: "connected", WhatsAppJID: "5511@wa"},
		},
		{
			name:  "both fields",
			input: UpdateInput{Name: strptr("novo"), ExternalRef: strptr("ref-2")},
			want:  model.Instance{Name: "novo", ExternalRef: "ref-2", Status: "connected", WhatsAppJID: "5511@wa"},
		},
		{
			name:  "empty external ref clears it",
			input: UpdateInput{ExternalRef: strptr("")},
			want:  model.Instance{Name: "antigo", ExternalRef: "", Status: "connected", WhatsAppJID: "5511@wa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := uuid.New()
			stored := model.Instance{ID: id, Name: "antigo", ExternalRef: "ref-1", Status: "connected", WhatsAppJID: "5511@wa"}
			repo := newFakeRepo(stored)
			svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

			updated, err := svc.Update(context.Background(), id, tt.input)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			tt.want.ID = id
			if !reflect.DeepEqual(*updated, tt.want) {
				t.Errorf("Update = %+v, want %+v", *updated, tt.want)
			}
			if len(repo.updateCalls) != 1 || !reflect.DeepEqual(repo.updateCalls[0], tt.want) {
				t.Errorf("repo Update calls = %+v, want %+v", repo.updateCalls, tt.want)
			}
		})
	}
}

func TestServiceUpdateNotFound(t *testing.T) {
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Update(context.Background(), uuid.New(), UpdateInput{Name: strptr("novo")})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update error = %v, want ErrNotFound", err)
	}
}

func TestServiceUpdateExternalRefTaken(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja"})
	repo.updateErr = fmt.Errorf("update instance: %w", storage.ErrExternalRefTaken)
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Update(context.Background(), id, UpdateInput{ExternalRef: strptr("crm-1")})
	if !errors.Is(err, ErrExternalRefTaken) {
		t.Fatalf("Update error = %v, want ErrExternalRefTaken", err)
	}
}

func TestServiceDeleteRemovesSessionMediaAndRow(t *testing.T) {
	id := uuid.New()
	order := []string{}
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja"})
	repo.order = &order
	sessions := &recordingManager{Fake: sessiontest.New(nil), order: &order}
	media := &fakeMedia{order: &order}
	svc := NewService(repo, sessions, media)

	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := sessions.RemoveCalls(); len(got) != 1 || got[0] != id {
		t.Errorf("session Remove calls = %v, want [%s]", got, id)
	}
	if len(media.calls) != 1 || media.calls[0] != id {
		t.Errorf("media DeleteByInstance calls = %v, want [%s]", media.calls, id)
	}
	if len(repo.deleteCalls) != 1 || repo.deleteCalls[0] != id {
		t.Errorf("repo Delete calls = %v, want [%s]", repo.deleteCalls, id)
	}
	if want := []string{"session", "media", "repo"}; !slices.Equal(order, want) {
		t.Errorf("operation order = %v, want %v", order, want)
	}
	if _, ok := repo.instances[id]; ok {
		t.Error("instance row still stored after Delete")
	}
}

func TestServiceDeleteNotFound(t *testing.T) {
	id := uuid.New()
	order := []string{}
	repo := newFakeRepo()
	repo.order = &order
	sessions := &recordingManager{Fake: sessiontest.New(nil), order: &order}
	media := &fakeMedia{order: &order}
	svc := NewService(repo, sessions, media)

	err := svc.Delete(context.Background(), id)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete error = %v, want ErrNotFound", err)
	}
	if len(order) != 0 || len(media.calls) != 0 || len(repo.deleteCalls) != 0 {
		t.Errorf("Delete on missing instance touched dependencies: order=%v media=%v repo=%v",
			order, media.calls, repo.deleteCalls)
	}
}

func TestServiceDeleteStopsWhenSessionRemovalFails(t *testing.T) {
	id := uuid.New()
	order := []string{}
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja"})
	repo.order = &order
	sessions := &recordingManager{Fake: sessiontest.New(nil), order: &order}
	sessions.RemoveErr = errors.New("delete credentials failed")
	media := &fakeMedia{order: &order}
	svc := NewService(repo, sessions, media)

	err := svc.Delete(context.Background(), id)
	if err == nil {
		t.Fatal("Delete error = nil, want the session removal failure")
	}
	if len(media.calls) != 0 || len(repo.deleteCalls) != 0 {
		t.Errorf("Delete continued after the session failure: media=%v repo=%v", media.calls, repo.deleteCalls)
	}
	if _, ok := repo.instances[id]; !ok {
		t.Error("instance row removed even though the session removal failed")
	}
}

func TestServiceDeleteStopsWhenMediaDeletionFails(t *testing.T) {
	id := uuid.New()
	order := []string{}
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja"})
	repo.order = &order
	sessions := &recordingManager{Fake: sessiontest.New(nil), order: &order}
	media := &fakeMedia{order: &order, err: errors.New("remove media failed")}
	svc := NewService(repo, sessions, media)

	err := svc.Delete(context.Background(), id)
	if err == nil {
		t.Fatal("Delete error = nil, want the media deletion failure")
	}
	if len(repo.deleteCalls) != 0 {
		t.Errorf("repo Delete calls = %v, want none after the media failure", repo.deleteCalls)
	}
	if _, ok := repo.instances[id]; !ok {
		t.Error("instance row removed even though the media deletion failed")
	}
}

func TestServiceConnectStartsPairing(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	sessions := sessiontest.New(nil)
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if result.Status != session.StatusPairing {
		t.Errorf("Status = %q, want %q", result.Status, session.StatusPairing)
	}
	wantQR := "fake-qr-" + id.String()
	if result.QRCode != wantQR {
		t.Errorf("QRCode = %q, want %q", result.QRCode, wantQR)
	}
	if result.QRExpiresAt == nil {
		t.Fatal("QRExpiresAt = nil, want the QR validity")
	}
	if result.QRExpiresAt.IsZero() {
		t.Error("QRExpiresAt is the zero time, want the QR validity")
	}

	stored := repo.instances[id]
	if stored.Status != string(session.StatusPairing) {
		t.Errorf("stored status = %q, want %q", stored.Status, session.StatusPairing)
	}
	if len(repo.setConnectionCalls) != 1 || repo.setConnectionCalls[0].status != string(session.StatusPairing) {
		t.Errorf("SetConnection calls = %+v, want one pairing update", repo.setConnectionCalls)
	}
	if len(repo.updateCalls) != 0 {
		t.Errorf("repo Update calls = %+v, want none (partial update preferred)", repo.updateCalls)
	}
	calls := sessions.CreateCalls()
	if len(calls) != 1 || calls[0].ID != id {
		t.Errorf("session Create calls = %+v, want the instance %s", calls, id)
	}
}

func TestServiceConnectAlreadyConnectedSkipsQR(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusConnected)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusConnected)
	sess.SetJID("5511999999999@s.whatsapp.net")
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if result.Status != session.StatusConnected {
		t.Errorf("Status = %q, want %q", result.Status, session.StatusConnected)
	}
	if result.QRCode != "" {
		t.Errorf("QRCode = %q, want empty for a connected instance", result.QRCode)
	}
	if result.QRExpiresAt != nil {
		t.Errorf("QRExpiresAt = %v, want nil for a connected instance", result.QRExpiresAt)
	}
	if got := sess.ConnectCalls(); got != 0 {
		t.Errorf("session Connect calls = %d, want 0 for a connected instance", got)
	}
	if len(repo.updateCalls) != 0 {
		t.Errorf("repo Update calls = %+v, want none", repo.updateCalls)
	}
}

func TestServiceConnectWhilePairingReturnsCurrentQR(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusPairing)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sessions.Put(id, sess)
	if _, _, err := sess.Connect(context.Background()); err != nil {
		t.Fatalf("setup session Connect: %v", err)
	}
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	wantQR := "fake-qr-" + id.String()
	if result.QRCode != wantQR {
		t.Errorf("QRCode = %q, want the open pairing code %q", result.QRCode, wantQR)
	}
	if result.Status != session.StatusPairing {
		t.Errorf("Status = %q, want %q", result.Status, session.StatusPairing)
	}
	if got := sess.ConnectCalls(); got != 1 {
		t.Errorf("session Connect calls = %d, want 1 (the setup call only)", got)
	}
}

func TestServiceConnectNotFound(t *testing.T) {
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Connect(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Connect error = %v, want ErrNotFound", err)
	}
}

func TestServiceConnectSessionFailure(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	sessions := sessiontest.New(nil)
	sessions.CreateErr = errors.New("open device store failed")
	svc := NewService(repo, sessions, &fakeMedia{})

	_, err := svc.Connect(context.Background(), id)
	if err == nil {
		t.Fatal("Connect error = nil, want the session failure")
	}
	if len(repo.updateCalls) != 0 {
		t.Errorf("repo Update calls = %+v, want none after the session failure", repo.updateCalls)
	}
}

func TestServiceQRReturnsCurrentCode(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusPairing)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sessions.Put(id, sess)
	if _, _, err := sess.Connect(context.Background()); err != nil {
		t.Fatalf("setup session Connect: %v", err)
	}
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.QR(context.Background(), id)
	if err != nil {
		t.Fatalf("QR: %v", err)
	}

	wantQR := "fake-qr-" + id.String()
	if result.QRCode != wantQR {
		t.Errorf("QRCode = %q, want %q", result.QRCode, wantQR)
	}
	if result.Status != session.StatusPairing {
		t.Errorf("Status = %q, want %q", result.Status, session.StatusPairing)
	}
	if result.QRExpiresAt == nil || result.QRExpiresAt.IsZero() {
		t.Errorf("QRExpiresAt = %v, want the QR validity", result.QRExpiresAt)
	}
	if got := sess.ConnectCalls(); got != 1 {
		t.Errorf("session Connect calls = %d, want 1 (the setup call only)", got)
	}
}

func TestServiceQRStartsPairingWhenNoCode(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	sessions := sessiontest.New(nil)
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.QR(context.Background(), id)
	if err != nil {
		t.Fatalf("QR: %v", err)
	}

	if result.Status != session.StatusPairing {
		t.Errorf("Status = %q, want %q", result.Status, session.StatusPairing)
	}
	if result.QRCode == "" {
		t.Error("QRCode is empty, want a fresh code")
	}
	if stored := repo.instances[id]; stored.Status != string(session.StatusPairing) {
		t.Errorf("stored status = %q, want %q", stored.Status, session.StatusPairing)
	}
}

func TestServiceQRWhilePairingWithoutCodeFails(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusPairing)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusPairing)
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	_, err := svc.QR(context.Background(), id)
	if err == nil {
		t.Fatal("QR error = nil, want the missing code failure")
	}
	if got := sess.ConnectCalls(); got != 0 {
		t.Errorf("session Connect calls = %d, want 0 while a pairing is open", got)
	}
}

func TestServiceQRAlreadyConnected(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: string(session.StatusConnected)})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusConnected)
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	_, err := svc.QR(context.Background(), id)
	if !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("QR error = %v, want ErrAlreadyConnected", err)
	}
}

func TestServiceQRNotFound(t *testing.T) {
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{})

	_, err := svc.QR(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("QR error = %v, want ErrNotFound", err)
	}
}

func TestServiceRestoreCallsRestoreAll(t *testing.T) {
	sessions := sessiontest.New(nil)
	svc := NewService(newFakeRepo(), sessions, &fakeMedia{})

	if err := svc.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := sessions.RestoreCalls(); got != 1 {
		t.Errorf("RestoreAll calls = %d, want 1", got)
	}
}

func TestServiceRestorePropagatesFailure(t *testing.T) {
	sessions := sessiontest.New(nil)
	sessions.RestoreAllErr = errors.New("listing instances failed")
	svc := NewService(newFakeRepo(), sessions, &fakeMedia{})

	err := svc.Restore(context.Background())
	if err == nil {
		t.Fatal("Restore error = nil, want the restore failure")
	}
	if !strings.Contains(err.Error(), "listing instances failed") {
		t.Errorf("Restore error = %v, want it to carry the cause", err)
	}
}

func TestServiceDisconnectClearsIdentity(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusConnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusConnected)
	sess.SetJID("5511999999999@s.whatsapp.net")
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	if err := svc.Disconnect(context.Background(), id); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if got := sess.DisconnectCalls(); got != 1 {
		t.Errorf("session Disconnect calls = %d, want 1", got)
	}
	if removed := sessions.RemoveCalls(); len(removed) != 1 || removed[0] != id {
		t.Errorf("session Remove calls = %v, want [%s] (credentials deleted)", removed, id)
	}
	stored := repo.instances[id]
	if stored.Status != string(session.StatusDisconnected) {
		t.Errorf("stored status = %q, want %q", stored.Status, session.StatusDisconnected)
	}
	if stored.WhatsAppJID != "" {
		t.Errorf("stored whatsapp_jid = %q, want empty", stored.WhatsAppJID)
	}
	if len(repo.setConnectionCalls) != 1 {
		t.Fatalf("SetConnection calls = %+v, want one", repo.setConnectionCalls)
	}
	call := repo.setConnectionCalls[0]
	if call.id != id || call.status != string(session.StatusDisconnected) || call.jid != "" {
		t.Errorf("SetConnection call = %+v, want disconnected with no JID", call)
	}
}

func TestServiceDisconnectNotFound(t *testing.T) {
	repo := newFakeRepo()
	sessions := sessiontest.New(nil)
	svc := NewService(repo, sessions, &fakeMedia{})

	err := svc.Disconnect(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Disconnect error = %v, want ErrNotFound", err)
	}
	if len(sessions.CreateCalls()) != 0 || len(repo.setConnectionCalls) != 0 {
		t.Errorf("Disconnect on missing instance touched dependencies: create=%d set=%d",
			len(sessions.CreateCalls()), len(repo.setConnectionCalls))
	}
}

func TestServiceDisconnectStopsOnStatusUpdateFailure(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Status: string(session.StatusConnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	repo.setConnectionErr = errors.New("database down")
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	err := svc.Disconnect(context.Background(), id)
	if err == nil {
		t.Fatal("Disconnect error = nil, want the status update failure")
	}
	if stored := repo.instances[id]; stored.Status != string(session.StatusConnected) {
		t.Errorf("stored status = %q, want the unchanged %q", stored.Status, session.StatusConnected)
	}
	if removed := sessions.RemoveCalls(); len(removed) != 0 {
		t.Errorf("session Remove calls = %v, want none before the row is cleared", removed)
	}
}

func TestServiceDisconnectPropagatesCredentialRemovalFailure(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Status: string(session.StatusConnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sessions.Put(id, sess)
	sessions.RemoveErr = errors.New("delete credentials failed")
	svc := NewService(repo, sessions, &fakeMedia{})

	err := svc.Disconnect(context.Background(), id)
	if err == nil {
		t.Fatal("Disconnect error = nil, want the credential removal failure")
	}
	if stored := repo.instances[id]; stored.Status != string(session.StatusDisconnected) || stored.WhatsAppJID != "" {
		t.Errorf("stored instance = %+v, want the cleared connection state", stored)
	}
}

func TestServiceConnectStopsOnStatusUpdateFailure(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{ID: id, Name: "loja", Status: "disconnected"})
	repo.setConnectionErr = errors.New("database down")
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{})

	_, err := svc.Connect(context.Background(), id)
	if err == nil {
		t.Fatal("Connect error = nil, want the pairing update failure")
	}
}

func TestServiceDisconnectStopsOnSessionFailure(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Status: string(session.StatusConnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(id, nil)
	sess.SetStatus(session.StatusConnected)
	sess.SetJID("5511999999999@s.whatsapp.net")
	sess.DisconnectErr = errors.New("disconnect failed")
	sessions.Put(id, sess)
	svc := NewService(repo, sessions, &fakeMedia{})

	err := svc.Disconnect(context.Background(), id)
	if err == nil {
		t.Fatal("Disconnect error = nil, want the session failure")
	}
	if len(repo.setConnectionCalls) != 0 {
		t.Errorf("SetConnection calls = %+v, want none after the session failure", repo.setConnectionCalls)
	}
	if removed := sessions.RemoveCalls(); len(removed) != 0 {
		t.Errorf("session Remove calls = %v, want none after the session failure", removed)
	}
	if stored := repo.instances[id]; stored.WhatsAppJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("stored whatsapp_jid = %q, want it unchanged", stored.WhatsAppJID)
	}
}

func TestServiceDisconnectWithoutDeviceClearsPairing(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusError),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := &stalePairingManager{Fake: sessiontest.New(nil), failures: 1}
	svc := NewService(repo, sessions, &fakeMedia{})

	if err := svc.Disconnect(context.Background(), id); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	stored := repo.instances[id]
	if stored.Status != string(session.StatusDisconnected) || stored.WhatsAppJID != "" {
		t.Errorf("stored instance = %+v, want disconnected without a JID", stored)
	}
	attempts := sessions.createAttempts()
	if len(attempts) != 2 {
		t.Fatalf("session Create attempts = %d, want 2 (stale then fresh)", len(attempts))
	}
	if attempts[0].WhatsAppJID != "5511999999999@s.whatsapp.net" || attempts[1].WhatsAppJID != "" {
		t.Errorf("Create attempts JIDs = %q/%q, want the stale JID then none",
			attempts[0].WhatsAppJID, attempts[1].WhatsAppJID)
	}
	if removed := sessions.RemoveCalls(); len(removed) != 2 || removed[1] != id {
		t.Errorf("session Remove calls = %v, want the reset and the final removal of %s", removed, id)
	}
}

func TestServiceConnectWithoutDeviceStartsFreshPairing(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusError),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := &stalePairingManager{Fake: sessiontest.New(nil), failures: 1}
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if result.Status != session.StatusPairing || result.QRCode != "fake-qr-"+id.String() {
		t.Errorf("Connect result = %+v, want a fresh pairing QR", result)
	}
	stored := repo.instances[id]
	if stored.Status != string(session.StatusPairing) || stored.WhatsAppJID != "" {
		t.Errorf("stored instance = %+v, want pairing without a JID", stored)
	}
	attempts := sessions.createAttempts()
	if len(attempts) != 2 || attempts[1].WhatsAppJID != "" {
		t.Errorf("Create attempts = %d, want the retry without a JID", len(attempts))
	}
}

func TestServiceQRWithoutDeviceStartsFreshPairing(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusError),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sessions := &stalePairingManager{Fake: sessiontest.New(nil), failures: 1}
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.QR(context.Background(), id)
	if err != nil {
		t.Fatalf("QR: %v", err)
	}
	if result.Status != session.StatusPairing || result.QRCode != "fake-qr-"+id.String() {
		t.Errorf("QR result = %+v, want a fresh pairing QR", result)
	}
	if stored := repo.instances[id]; stored.WhatsAppJID != "" {
		t.Errorf("stored whatsapp_jid = %q, want it cleared", stored.WhatsAppJID)
	}
}

func TestServiceConnectResetsSessionWithoutDevice(t *testing.T) {
	id := uuid.New()
	repo := newFakeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusDisconnected),
		WhatsAppJID: "5511999999999@s.whatsapp.net",
	})
	sess := &oneShotNoDeviceSession{FakeSession: sessiontest.NewSession(id, nil), fails: 1}
	sessions := &staleConnectManager{Fake: sessiontest.New(nil), sess: sess}
	svc := NewService(repo, sessions, &fakeMedia{})

	result, err := svc.Connect(context.Background(), id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if result.Status != session.StatusPairing {
		t.Errorf("Connect status = %q, want %q after the reset", result.Status, session.StatusPairing)
	}
	if got := sess.ConnectCalls(); got != 1 {
		t.Errorf("successful session Connect calls = %d, want 1 (the retry)", got)
	}
	if stored := repo.instances[id]; stored.Status != string(session.StatusPairing) || stored.WhatsAppJID != "" {
		t.Errorf("stored instance = %+v, want pairing without a JID", stored)
	}
}
