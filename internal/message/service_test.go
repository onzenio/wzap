package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

// fakeInstances is an in-memory InstanceReader that records the lookups and can
// force a storage failure.
type fakeInstances struct {
	instances map[uuid.UUID]model.Instance
	getErr    error
	gets      []uuid.UUID
}

// Get returns the stored instance or storage.ErrNotFound.
func (f *fakeInstances) Get(_ context.Context, id uuid.UUID) (*model.Instance, error) {
	f.gets = append(f.gets, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	instance, ok := f.instances[id]
	if !ok {
		return nil, fmt.Errorf("get instance: %w", storage.ErrNotFound)
	}
	return &instance, nil
}

// fakeResolver records the recipient resolutions and answers with the
// configured outcome, defaulting to a fixed JID.
type fakeResolver struct {
	resolveFn func(ctx context.Context, instanceID uuid.UUID, phone string) (string, error)
	calls     []resolveCall
}

// resolveCall records one Resolve invocation.
type resolveCall struct {
	instanceID uuid.UUID
	phone      string
}

// Resolve records the call and returns the configured result.
func (f *fakeResolver) Resolve(ctx context.Context, instanceID uuid.UUID, phone string) (string, error) {
	f.calls = append(f.calls, resolveCall{instanceID: instanceID, phone: phone})
	if f.resolveFn != nil {
		return f.resolveFn(ctx, instanceID, phone)
	}
	return "5547988359190@s.whatsapp.net", nil
}

// fakeMessages is an in-memory MessageStore that records the writes and reads
// and lets tests force storage failures.
type fakeMessages struct {
	created   []model.OutboundMessage
	createErr error

	getResult *model.OutboundMessage
	getErr    error
	getIDs    []uuid.UUID

	listResult []model.OutboundMessage
	listNext   string
	listErr    error
	listIDs    []uuid.UUID
	listLimit  int
	listCursor string
}

// Create stores msg and returns it, or the forced error when set.
func (f *fakeMessages) Create(_ context.Context, msg model.OutboundMessage) (*model.OutboundMessage, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, msg)
	stored := msg
	return &stored, nil
}

// Get returns the configured message, or the forced error when set, defaulting
// to storage.ErrNotFound.
func (f *fakeMessages) Get(_ context.Context, id uuid.UUID) (*model.OutboundMessage, error) {
	f.getIDs = append(f.getIDs, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getResult == nil {
		return nil, fmt.Errorf("get message: %w", storage.ErrNotFound)
	}
	stored := *f.getResult
	return &stored, nil
}

// ListByInstance returns the configured page and records the pagination
// arguments, or the forced error when set.
func (f *fakeMessages) ListByInstance(_ context.Context, instanceID uuid.UUID, limit int, cursor string) ([]model.OutboundMessage, string, error) {
	f.listIDs = append(f.listIDs, instanceID)
	f.listLimit = limit
	f.listCursor = cursor
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	return f.listResult, f.listNext, nil
}

// serviceFixture builds the service under test over fakes plus the connected
// instance it enqueues to.
type serviceFixture struct {
	service   *Service
	instances *fakeInstances
	resolver  *fakeResolver
	messages  *fakeMessages
	instance  uuid.UUID
}

// newServiceFixture returns a fixture whose instance has the given status.
func newServiceFixture(status session.Status) *serviceFixture {
	instanceID := uuid.New()
	instances := &fakeInstances{instances: map[uuid.UUID]model.Instance{
		instanceID: {ID: instanceID, Name: "loja", Status: string(status)},
	}}
	resolver := &fakeResolver{}
	messages := &fakeMessages{}
	return &serviceFixture{
		service:   NewService(instances, resolver, messages),
		instances: instances,
		resolver:  resolver,
		messages:  messages,
		instance:  instanceID,
	}
}

// decodePayload decodes a stored message payload into a generic map.
func decodePayload(t *testing.T, msg model.OutboundMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("decode payload %q: %v", msg.Payload, err)
	}
	return payload
}

func TestEnqueueTextSuccess(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	id, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "+55 (47) 98835-9190",
		Text: "olá",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if len(f.messages.created) != 1 {
		t.Fatalf("created = %d messages, want 1", len(f.messages.created))
	}
	created := f.messages.created[0]
	if id != created.ID {
		t.Errorf("Enqueue id = %s, want the created message id %s", id, created.ID)
	}
	if created.InstanceID != f.instance {
		t.Errorf("instance = %s, want %s", created.InstanceID, f.instance)
	}
	if created.Status != StatusQueued {
		t.Errorf("status = %q, want %q", created.Status, StatusQueued)
	}
	if created.Type != TypeText {
		t.Errorf("type = %q, want %q", created.Type, TypeText)
	}
	if created.RecipientJID != "5547988359190@s.whatsapp.net" {
		t.Errorf("recipient = %q, want the resolved JID", created.RecipientJID)
	}
	if len(f.resolver.calls) != 1 {
		t.Fatalf("Resolve calls = %d, want 1", len(f.resolver.calls))
	}
	if got := f.resolver.calls[0]; got.instanceID != f.instance || got.phone != "+55 (47) 98835-9190" {
		t.Errorf("Resolve(%s, %q), want the instance and the raw request phone", got.instanceID, got.phone)
	}
	if payload := decodePayload(t, created); payload["text"] != "olá" {
		t.Errorf("payload.text = %v, want %q", payload["text"], "olá")
	}
}

func TestEnqueueLocationSuccess(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type:      TypeLocation,
		To:        "5547988359190",
		Latitude:  -23.55,
		Longitude: -46.63,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	payload := decodePayload(t, f.messages.created[0])
	if payload["latitude"] != -23.55 || payload["longitude"] != -46.63 {
		t.Errorf("payload = %v, want latitude -23.55 and longitude -46.63", payload)
	}
}

func TestEnqueueLocationRejectsOutOfRange(t *testing.T) {
	tests := []struct {
		name      string
		latitude  float64
		longitude float64
	}{
		{name: "latitude too high", latitude: 90.1, longitude: 0},
		{name: "latitude too low", latitude: -90.1, longitude: 0},
		{name: "longitude too high", latitude: 0, longitude: 180.1},
		{name: "longitude too low", latitude: 0, longitude: -180.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newServiceFixture(session.StatusConnected)

			_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
				Type:      TypeLocation,
				To:        "5547988359190",
				Latitude:  tt.latitude,
				Longitude: tt.longitude,
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Enqueue error = %v, want ErrInvalidInput", err)
			}
			if len(f.messages.created) != 0 {
				t.Errorf("created = %d messages, want none", len(f.messages.created))
			}
			if len(f.resolver.calls) != 0 {
				t.Errorf("Resolve calls = %d, want none", len(f.resolver.calls))
			}
		})
	}
}

func TestEnqueueContactSuccess(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type:        TypeContact,
		To:          "5547988359190",
		DisplayName: "Fulano",
		VCard:       "BEGIN:VCARD\nVERSION:3.0\nEND:VCARD",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	payload := decodePayload(t, f.messages.created[0])
	if payload["display_name"] != "Fulano" {
		t.Errorf("payload.display_name = %v, want %q", payload["display_name"], "Fulano")
	}
	if payload["vcard"] != "BEGIN:VCARD\nVERSION:3.0\nEND:VCARD" {
		t.Errorf("payload.vcard = %v, want the vcard", payload["vcard"])
	}
}

func TestEnqueueContactRequiresDisplayNameAndVCard(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		vcard       string
	}{
		{name: "missing display name", vcard: "BEGIN:VCARD\nEND:VCARD"},
		{name: "missing vcard", displayName: "Fulano"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newServiceFixture(session.StatusConnected)

			_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
				Type:        TypeContact,
				To:          "5547988359190",
				DisplayName: tt.displayName,
				VCard:       tt.vcard,
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Enqueue error = %v, want ErrInvalidInput", err)
			}
			if len(f.messages.created) != 0 {
				t.Errorf("created = %d messages, want none", len(f.messages.created))
			}
		})
	}
}

func TestEnqueueMediaSuccess(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	mediaID := uuid.New()

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type:     TypeMedia,
		To:       "5547988359190",
		Caption:  "olha isso",
		Filename: "foto.jpg",
		MediaID:  &mediaID,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	created := f.messages.created[0]
	if created.MediaID == nil || *created.MediaID != mediaID {
		t.Errorf("media_id = %v, want %s", created.MediaID, mediaID)
	}
	payload := decodePayload(t, created)
	if payload["caption"] != "olha isso" || payload["filename"] != "foto.jpg" {
		t.Errorf("payload = %v, want caption and filename", payload)
	}
}

func TestEnqueueMediaRequiresMediaID(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeMedia,
		To:   "5547988359190",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Enqueue error = %v, want ErrInvalidInput", err)
	}
	if len(f.messages.created) != 0 {
		t.Errorf("created = %d messages, want none", len(f.messages.created))
	}
}

func TestEnqueueTextCarriesQuotedID(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type:     TypeText,
		To:       "5547988359190",
		Text:     "resposta",
		QuotedID: "WA-ORIG-1",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	payload := decodePayload(t, f.messages.created[0])
	if payload["text"] != "resposta" {
		t.Errorf("payload.text = %v, want %q", payload["text"], "resposta")
	}
	if payload["quoted_id"] != "WA-ORIG-1" {
		t.Errorf("payload.quoted_id = %v, want %q", payload["quoted_id"], "WA-ORIG-1")
	}
}

func TestEnqueueTextOmitsQuotedIDWhenUnquoted(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "olá",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if payload := decodePayload(t, f.messages.created[0]); payload["quoted_id"] != nil {
		t.Errorf("payload.quoted_id = %v, want absent for an unquoted message", payload["quoted_id"])
	}
}

func TestEnqueueMediaCarriesQuotedID(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	mediaID := uuid.New()

	if _, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type:     TypeMedia,
		To:       "5547988359190",
		Caption:  "olha",
		MediaID:  &mediaID,
		QuotedID: "WA-ORIG-2",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	payload := decodePayload(t, f.messages.created[0])
	if payload["quoted_id"] != "WA-ORIG-2" {
		t.Errorf("payload.quoted_id = %v, want %q", payload["quoted_id"], "WA-ORIG-2")
	}
}

func TestEnqueueRejectsUnsupportedType(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: "sticker",
		To:   "5547988359190",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Enqueue error = %v, want ErrInvalidInput", err)
	}
	if len(f.resolver.calls) != 0 {
		t.Errorf("Resolve calls = %d, want none for an invalid payload", len(f.resolver.calls))
	}
}

func TestEnqueueRejectsBlankText(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "   ",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Enqueue error = %v, want ErrInvalidInput", err)
	}
	if len(f.messages.created) != 0 {
		t.Errorf("created = %d messages, want none", len(f.messages.created))
	}
}

func TestEnqueueDisconnectedInstance(t *testing.T) {
	for _, status := range []session.Status{session.StatusDisconnected, session.StatusPairing, session.StatusError} {
		t.Run(string(status), func(t *testing.T) {
			f := newServiceFixture(status)

			_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
				Type: TypeText,
				To:   "5547988359190",
				Text: "olá",
			})
			if !errors.Is(err, ErrInstanceNotConnected) {
				t.Fatalf("Enqueue error = %v, want ErrInstanceNotConnected", err)
			}
			if len(f.resolver.calls) != 0 {
				t.Errorf("Resolve calls = %d, want none", len(f.resolver.calls))
			}
			if len(f.messages.created) != 0 {
				t.Errorf("created = %d messages, want none", len(f.messages.created))
			}
		})
	}
}

func TestEnqueueInstanceNotFound(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	_, err := f.service.Enqueue(context.Background(), uuid.New(), EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "olá",
	})
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Enqueue error = %v, want ErrInstanceNotFound", err)
	}
	if len(f.messages.created) != 0 {
		t.Errorf("created = %d messages, want none", len(f.messages.created))
	}
}

func TestEnqueueInstanceLookupFailure(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.instances.getErr = errors.New("database down")

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "olá",
	})
	if err == nil || errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Enqueue error = %v, want a wrapped storage failure", err)
	}
}

func TestEnqueueNumberNotFound(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.resolver.resolveFn = func(context.Context, uuid.UUID, string) (string, error) {
		return "", fmt.Errorf("%w: 5547999999999", ErrNumberNotFound)
	}

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547999999999",
		Text: "olá",
	})
	if !errors.Is(err, ErrNumberNotFound) {
		t.Fatalf("Enqueue error = %v, want ErrNumberNotFound", err)
	}
	if len(f.messages.created) != 0 {
		t.Errorf("created = %d messages, want none", len(f.messages.created))
	}
}

func TestEnqueueResolverUnavailable(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.resolver.resolveFn = func(context.Context, uuid.UUID, string) (string, error) {
		return "", fmt.Errorf("%w: session down", ErrResolverUnavailable)
	}

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "olá",
	})
	if !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("Enqueue error = %v, want ErrResolverUnavailable", err)
	}
}

func TestEnqueueStoreFailure(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.messages.createErr = errors.New("insert failed")

	_, err := f.service.Enqueue(context.Background(), f.instance, EnqueueInput{
		Type: TypeText,
		To:   "5547988359190",
		Text: "olá",
	})
	if err == nil {
		t.Fatal("Enqueue succeeded, want a storage failure")
	}
}

func TestGetMessage(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	messageID := uuid.New()
	f.messages.getResult = &model.OutboundMessage{
		ID:         messageID,
		InstanceID: f.instance,
		Type:       TypeText,
		Status:     StatusQueued,
	}

	found, err := f.service.Get(context.Background(), f.instance, messageID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found.ID != messageID {
		t.Errorf("Get id = %s, want %s", found.ID, messageID)
	}
}

func TestGetMessageHiddenForAnotherInstance(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	messageID := uuid.New()
	f.messages.getResult = &model.OutboundMessage{
		ID:         messageID,
		InstanceID: uuid.New(),
		Type:       TypeText,
		Status:     StatusQueued,
	}

	_, err := f.service.Get(context.Background(), f.instance, messageID)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("Get error = %v, want ErrMessageNotFound", err)
	}
}

func TestGetMessageNotFound(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)

	_, err := f.service.Get(context.Background(), f.instance, uuid.New())
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("Get error = %v, want ErrMessageNotFound", err)
	}
}

func TestListMessages(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.messages.listResult = []model.OutboundMessage{{ID: uuid.New(), InstanceID: f.instance, Status: StatusQueued}}
	f.messages.listNext = "cursor-1"

	items, next, err := f.service.List(context.Background(), f.instance, 25, "cursor-0")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}
	if next != "cursor-1" {
		t.Errorf("next = %q, want %q", next, "cursor-1")
	}
	if len(f.messages.listIDs) != 1 || f.messages.listIDs[0] != f.instance {
		t.Errorf("ListByInstance instance = %v, want %s", f.messages.listIDs, f.instance)
	}
	if f.messages.listLimit != 25 || f.messages.listCursor != "cursor-0" {
		t.Errorf("ListByInstance(%d, %q), want (25, %q)", f.messages.listLimit, f.messages.listCursor, "cursor-0")
	}
}

func TestListMessagesInvalidCursor(t *testing.T) {
	f := newServiceFixture(session.StatusConnected)
	f.messages.listErr = fmt.Errorf("list messages: %w", storage.ErrInvalidCursor)

	_, _, err := f.service.List(context.Background(), f.instance, 25, "not-a-uuid")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("List error = %v, want ErrInvalidCursor", err)
	}
}
