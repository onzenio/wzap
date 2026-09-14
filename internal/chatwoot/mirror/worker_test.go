// Package mirror mirrors WhatsApp inbound events into Chatwoot in real time.
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/client"
	"wzap/internal/chatwoot/contacts"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
	"wzap/internal/storage/postgres"
	"wzap/internal/storage/postgres/postgrestest"
)

// fakeChatwootClient records Chatwoot calls and replays canned answers.
type fakeChatwootClient struct {
	mu            sync.Mutex
	inboxes       []client.Inbox
	nextMessageID int64

	createCalls     []createCall
	attachmentCalls []attachmentCall
	deleteCalls     []deleteCall
	lastSeenCalls   []int64
	deleteErr       error
	createErr       error
}

type createCall struct {
	conversationID int64
	req            client.CreateMessageRequest
}

type attachmentCall struct {
	conversationID int64
	req            client.CreateMessageWithAttachmentRequest
}

type deleteCall struct {
	conversationID int64
	messageID      int64
}

func (f *fakeChatwootClient) ListInboxes(context.Context) ([]client.Inbox, error) {
	return f.inboxes, nil
}

func (f *fakeChatwootClient) CreateMessage(_ context.Context, conversationID int64, req client.CreateMessageRequest) (*client.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextMessageID++
	f.createCalls = append(f.createCalls, createCall{conversationID: conversationID, req: req})
	return &client.Message{ID: f.nextMessageID, Content: req.Content}, nil
}

func (f *fakeChatwootClient) CreateMessageWithAttachment(_ context.Context, conversationID int64, req client.CreateMessageWithAttachmentRequest) (*client.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextMessageID++
	f.attachmentCalls = append(f.attachmentCalls, attachmentCall{conversationID: conversationID, req: req})
	return &client.Message{ID: f.nextMessageID, Content: req.Content}, nil
}

func (f *fakeChatwootClient) DeleteMessage(_ context.Context, conversationID, messageID int64) (struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return struct{}{}, f.deleteErr
	}
	f.deleteCalls = append(f.deleteCalls, deleteCall{conversationID: conversationID, messageID: messageID})
	return struct{}{}, nil
}

func (f *fakeChatwootClient) UpdateLastSeen(_ context.Context, conversationID int64) (struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSeenCalls = append(f.lastSeenCalls, conversationID)
	return struct{}{}, nil
}

func (f *fakeChatwootClient) creates() []createCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]createCall(nil), f.createCalls...)
}

func (f *fakeChatwootClient) attachments() []attachmentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attachmentCall(nil), f.attachmentCalls...)
}

// fakeContactResolver replays one contact or a resolution failure.
type fakeContactResolver struct {
	contact   *contacts.Contact
	err       error
	lastPhone string
	lastIsGrp bool
	lastJID   string
}

func (f *fakeContactResolver) Resolve(_ context.Context, phone string, isGroup bool, _, _, jid string) (*contacts.Contact, error) {
	f.lastPhone, f.lastIsGrp, f.lastJID = phone, isGroup, jid
	return f.contact, f.err
}

// fakeConversationResolver replays one conversation id or a failure.
type fakeConversationResolver struct {
	id      int64
	err     error
	lastJID string
	calls   int
}

func (f *fakeConversationResolver) Resolve(_ context.Context, _ uuid.UUID, remoteJID string, _ int64) (int64, error) {
	f.calls++
	f.lastJID = remoteJID
	return f.id, f.err
}

// fakeMediaStore replays stored bytes or an open failure.
type fakeMediaStore struct {
	data []byte
	name string
	mime string
	err  error
}

func (f *fakeMediaStore) Open(_ context.Context, _ uuid.UUID) (io.ReadCloser, *model.Media, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.data)), &model.Media{
		Mimetype: f.mime,
		Filename: f.name,
	}, nil
}

// fakeConfigs replays one connector configuration.
type fakeConfigs struct {
	cfg *model.ChatwootConfig
	err error
}

func (f *fakeConfigs) Get(_ context.Context, _ uuid.UUID) (*model.ChatwootConfig, error) {
	return f.cfg, f.err
}

func (f *fakeConfigs) Put(_ context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error) {
	return &cfg, nil
}

func (f *fakeConfigs) Delete(_ context.Context, _ uuid.UUID) error { return nil }

// fakeMessages is an in-memory ChatwootMessageRepository.
type fakeMessages struct {
	mu   sync.Mutex
	rows map[string]model.ChatwootMessage
	// failPuts makes the next failPuts Put calls fail with putErr (or a
	// generic error when putErr is nil), to simulate a post-Create store
	// outage in the duplicate-window tests.
	failPuts int
	putErr   error
}

func newFakeMessages() *fakeMessages { return &fakeMessages{rows: map[string]model.ChatwootMessage{}} }

func (f *fakeMessages) key(instanceID uuid.UUID, waKey string) string {
	return instanceID.String() + "|" + waKey
}

func (f *fakeMessages) Put(_ context.Context, msg model.ChatwootMessage) (*model.ChatwootMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPuts > 0 {
		f.failPuts--
		err := f.putErr
		if err == nil {
			err = errors.New("fake correlation store down")
		}
		return nil, err
	}
	f.rows[f.key(msg.InstanceID, msg.WAKey)] = msg
	stored := msg
	return &stored, nil
}

func (f *fakeMessages) GetByWAKey(_ context.Context, instanceID uuid.UUID, waKey string) (*model.ChatwootMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg, ok := f.rows[f.key(instanceID, waKey)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	stored := msg
	return &stored, nil
}

func (f *fakeMessages) DeleteByInstance(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}

// testFixture bundles a worker with its fakes.
type testFixture struct {
	worker *Worker
	cli    *fakeChatwootClient
	cts    *fakeContactResolver
	convs  *fakeConversationResolver
	media  *fakeMediaStore
	msgs   *fakeMessages
}

func newFixture(cfg *model.ChatwootConfig) *testFixture {
	fx := &testFixture{
		cli:   &fakeChatwootClient{inboxes: []client.Inbox{{ID: 5, Name: "support"}}},
		cts:   &fakeContactResolver{contact: &contacts.Contact{ID: 11, PhoneNumber: "+5511999999999"}},
		convs: &fakeConversationResolver{id: 22},
		media: &fakeMediaStore{data: []byte("fake-bytes"), name: "photo.jpg", mime: "image/jpeg"},
		msgs:  newFakeMessages(),
	}
	if cfg == nil {
		cfg = &model.ChatwootConfig{
			InstanceID: uuid.New(),
			Enabled:    true,
			URL:        "https://chatwoot.example",
			AccountID:  "1",
			Token:      "token",
			NameInbox:  "support",
		}
	}
	configs := &fakeConfigs{cfg: cfg}
	fx.worker = New(Deps{
		Configs:  configs,
		Messages: fx.msgs,
		Media:    fx.media,
		Global:   config.Chatwoot{Enabled: true, MessageRead: true, MessageDelete: true},
		ClientFor: func(model.ChatwootConfig) ChatwootClient {
			return fx.cli
		},
		ContactsFor: func(ChatwootClient, model.ChatwootConfig) ContactResolver {
			return fx.cts
		},
		ConversationsFor: func(ChatwootClient, model.ChatwootConfig, int64) ConversationResolver {
			return fx.convs
		},
	})
	return fx
}

func testMessagePayload() MessagePayload {
	return MessagePayload{
		FromJID:   "5511999999999@s.whatsapp.net",
		ChatJID:   "5511999999999@s.whatsapp.net",
		MessageID: "WA-MSG-1",
		Type:      "text",
		Text:      "olá, *mundo*",
		Timestamp: time.Now().UTC(),
	}
}

func TestHandleMessageMirrorsTextWithSourceID(t *testing.T) {
	fx := newFixture(nil)
	instanceID := uuid.New()
	eventID := uuid.New()

	if err := fx.worker.HandleMessage(context.Background(), instanceID, eventID, testMessagePayload()); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	calls := fx.cli.creates()
	if len(calls) != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.conversationID != 22 {
		t.Errorf("conversation = %d, want 22", got.conversationID)
	}
	if want := "WAID:WA-MSG-1"; got.req.SourceID != want {
		t.Errorf("source_id = %q, want %q", got.req.SourceID, want)
	}
	if len(got.req.ContentAttributes) == 0 {
		t.Error("content_attributes is empty, want WA correlation")
	}
	if got.req.MessageType != client.MessageTypeIncoming {
		t.Errorf("message_type = %q, want incoming", got.req.MessageType)
	}
	if got.req.Content == "" || got.req.Content == "olá, *mundo*" {
		t.Errorf("content = %q, want markdown converted to Chatwoot form", got.req.Content)
	}

	stored, err := fx.msgs.GetByWAKey(context.Background(), instanceID, "WA-MSG-1")
	if err != nil {
		t.Fatalf("correlation missing: %v", err)
	}
	if stored.ChatwootMessageID != 1 || stored.ConversationID != 22 || stored.InboxID != 5 {
		t.Errorf("correlation = %+v, want message 1 in conversation 22 of inbox 5", stored)
	}
}

func TestHandleMessageSkipsIgnoredAndBroadcast(t *testing.T) {
	cfg := &model.ChatwootConfig{
		InstanceID: uuid.New(),
		Enabled:    true,
		URL:        "https://chatwoot.example",
		AccountID:  "1",
		Token:      "token",
		NameInbox:  "support",
		IgnoreJIDs: []string{"5511000000000@s.whatsapp.net"},
	}
	fx := newFixture(cfg)
	ctx := context.Background()

	ignored := testMessagePayload()
	ignored.FromJID = "5511000000000@s.whatsapp.net"
	ignored.ChatJID = "5511000000000@s.whatsapp.net"
	if err := fx.worker.HandleMessage(ctx, cfg.InstanceID, uuid.New(), ignored); err != nil {
		t.Fatalf("ignored JID: %v", err)
	}

	broadcast := testMessagePayload()
	broadcast.ChatJID = "status@broadcast"
	if err := fx.worker.HandleMessage(ctx, cfg.InstanceID, uuid.New(), broadcast); err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	if n := len(fx.cli.creates()); n != 0 {
		t.Errorf("CreateMessage calls = %d, want 0 (ignored + broadcast skipped)", n)
	}
}

func TestHandleMessageSkipsWhenContactCreationFails(t *testing.T) {
	fx := newFixture(nil)
	fx.cts.contact, fx.cts.err = nil, errors.New("chatwoot 500")

	if err := fx.worker.HandleMessage(context.Background(), uuid.New(), uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("contact failure must skip, got error: %v", err)
	}
	if n := len(fx.cli.creates()); n != 0 {
		t.Errorf("CreateMessage calls = %d, want 0 (message skipped)", n)
	}
}

func TestHandleMessageRedeliveryIsIdempotent(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()

	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Same payload with a fresh event id (broker redelivery after ack loss).
	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	// Same event id twice (in-memory dedup path): replay carries the same
	// payload the broker redelivered.
	second := testMessagePayload()
	second.MessageID = "WA-MSG-2"
	secondEvent := uuid.New()
	if err := fx.worker.HandleMessage(ctx, instanceID, secondEvent, second); err != nil {
		t.Fatalf("second message: %v", err)
	}
	if err := fx.worker.HandleMessage(ctx, instanceID, secondEvent, second); err != nil {
		t.Fatalf("same-event redelivery: %v", err)
	}

	if n := len(fx.cli.creates()); n != 2 {
		t.Errorf("CreateMessage calls = %d, want 2 (one per WA key, no duplicates)", n)
	}
}

func TestHandleMessageWithMediaUploadsAttachment(t *testing.T) {
	fx := newFixture(nil)
	msg := testMessagePayload()
	msg.Type = "image"
	msg.Media = &MediaRef{MediaID: uuid.New(), Mimetype: "image/jpeg", Filename: "photo.jpg"}

	if err := fx.worker.HandleMessage(context.Background(), uuid.New(), uuid.New(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	atts := fx.cli.attachments()
	if len(atts) != 1 {
		t.Fatalf("attachment calls = %d, want 1", len(atts))
	}
	got := atts[0].req
	if string(got.File) != "fake-bytes" || got.FileName != "photo.jpg" || got.ContentType != "image/jpeg" {
		t.Errorf("attachment = %+v, want stored bytes with name and mime", got)
	}
	if want := "WAID:WA-MSG-1"; got.SourceID != want {
		t.Errorf("source_id = %q, want %q", got.SourceID, want)
	}
}

func TestHandleMessageMediaOmittedPassesTextThrough(t *testing.T) {
	fx := newFixture(nil)
	fx.media.err = errors.New("media expired")
	msg := testMessagePayload()
	msg.Type = "image"
	msg.Text = "foto que expirou"
	msg.Media = &MediaRef{MediaID: uuid.New(), Mimetype: "image/jpeg"}
	msg.MediaOmitted = &MediaOmission{Reason: "media expired"}

	if err := fx.worker.HandleMessage(context.Background(), uuid.New(), uuid.New(), msg); err != nil {
		t.Fatalf("omitted media must not fail the mirror: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (text passes through)", n)
	}
	if n := len(fx.cli.attachments()); n != 0 {
		t.Errorf("attachment calls = %d, want 0", n)
	}
}

func TestHandleMessageGroupPrefixesSender(t *testing.T) {
	fx := newFixture(nil)
	msg := testMessagePayload()
	msg.IsGroup = true
	msg.ChatJID = "120363000000000000@g.us"
	msg.FromJID = "5511888888888@s.whatsapp.net"

	if err := fx.worker.HandleMessage(context.Background(), uuid.New(), uuid.New(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	calls := fx.cli.creates()
	if len(calls) != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1", len(calls))
	}
	if got := calls[0].req.Content; len(got) < len("5511888888888") || got[:13] != "5511888888888" {
		t.Errorf("group content = %q, want phone prefix", got)
	}
	if !fx.cts.lastIsGrp {
		t.Error("group chat did not resolve as a group contact")
	}
}

func TestHandleMessageUnknownTypeWarnsAndSkips(t *testing.T) {
	fx := newFixture(nil)
	msg := testMessagePayload()
	msg.Type = "reaction"
	msg.Text = ""

	if err := fx.worker.HandleMessage(context.Background(), uuid.New(), uuid.New(), msg); err != nil {
		t.Fatalf("unknown type must skip without error: %v", err)
	}
	if n := len(fx.cli.creates()); n != 0 {
		t.Errorf("CreateMessage calls = %d, want 0", n)
	}
}

func TestHandleEditCreatesLinkedEditedMessage(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()

	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("original: %v", err)
	}
	edit := EditPayload{
		FromJID:   "5511999999999@s.whatsapp.net",
		ChatJID:   "5511999999999@s.whatsapp.net",
		MessageID: "WA-MSG-1",
		Text:      "olá, *mundo editado*",
		Timestamp: time.Now().UTC(),
	}
	if err := fx.worker.HandleEdit(ctx, instanceID, uuid.New(), edit); err != nil {
		t.Fatalf("HandleEdit: %v", err)
	}

	calls := fx.cli.creates()
	if len(calls) != 2 {
		t.Fatalf("CreateMessage calls = %d, want 2 (original + edit)", len(calls))
	}
	got := calls[1].req
	if got.SourceReplyID != "1" {
		t.Errorf("source_reply_id = %q, want the original Chatwoot id", got.SourceReplyID)
	}
	if !contains(got.Content, "editada") {
		t.Errorf("edit content = %q, want an (editada) marker", got.Content)
	}
}

func TestHandleEditWithoutCorrelationSkips(t *testing.T) {
	fx := newFixture(nil)
	edit := EditPayload{
		FromJID:   "5511999999999@s.whatsapp.net",
		ChatJID:   "5511999999999@s.whatsapp.net",
		MessageID: "WA-UNKNOWN",
		Text:      "tarde demais",
	}

	if err := fx.worker.HandleEdit(context.Background(), uuid.New(), uuid.New(), edit); err != nil {
		t.Fatalf("missing correlation must skip without error: %v", err)
	}
	if n := len(fx.cli.creates()); n != 0 {
		t.Errorf("CreateMessage calls = %d, want 0", n)
	}
}

func TestHandleDeleteRemovesMirrorWhenEnabled(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()

	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("original: %v", err)
	}
	del := DeletePayload{
		FromJID:   "5511999999999@s.whatsapp.net",
		ChatJID:   "5511999999999@s.whatsapp.net",
		MessageID: "WA-MSG-1",
		Timestamp: time.Now().UTC(),
	}
	if err := fx.worker.HandleDelete(ctx, instanceID, uuid.New(), del); err != nil {
		t.Fatalf("HandleDelete: %v", err)
	}
	if len(fx.cli.deleteCalls) != 1 {
		t.Fatalf("DeleteMessage calls = %d, want 1", len(fx.cli.deleteCalls))
	}
	if got := fx.cli.deleteCalls[0]; got.conversationID != 22 || got.messageID != 1 {
		t.Errorf("delete = %+v, want conversation 22 message 1", got)
	}
}

func TestHandleDeleteGatedByFlag(t *testing.T) {
	fx := newFixture(nil)
	fx.worker.global.MessageDelete = false
	ctx := context.Background()
	instanceID := uuid.New()

	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("original: %v", err)
	}
	del := DeletePayload{MessageID: "WA-MSG-1", ChatJID: "5511999999999@s.whatsapp.net"}
	if err := fx.worker.HandleDelete(ctx, instanceID, uuid.New(), del); err != nil {
		t.Fatalf("disabled delete must skip without error: %v", err)
	}
	if len(fx.cli.deleteCalls) != 0 {
		t.Errorf("DeleteMessage calls = %d, want 0 (flag off)", len(fx.cli.deleteCalls))
	}
}

func TestHandleReadUpdatesLastSeen(t *testing.T) {
	fx := newFixture(nil)
	read := ReadPayload{
		ChatJID:    "5511999999999@s.whatsapp.net",
		Status:     "read",
		MessageIDs: []string{"WA-MSG-1"},
		Timestamp:  time.Now().UTC(),
	}
	if err := fx.worker.HandleRead(context.Background(), uuid.New(), uuid.New(), read); err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	if len(fx.cli.lastSeenCalls) != 1 || fx.cli.lastSeenCalls[0] != 22 {
		t.Errorf("last_seen calls = %v, want [22]", fx.cli.lastSeenCalls)
	}
}

func TestHandleReadSkipsDeliveredAndDisabled(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()

	delivered := ReadPayload{ChatJID: "5511999999999@s.whatsapp.net", Status: "delivered"}
	if err := fx.worker.HandleRead(ctx, uuid.New(), uuid.New(), delivered); err != nil {
		t.Fatalf("delivered: %v", err)
	}

	fx.worker.global.MessageRead = false
	read := ReadPayload{ChatJID: "5511999999999@s.whatsapp.net", Status: "read"}
	if err := fx.worker.HandleRead(ctx, uuid.New(), uuid.New(), read); err != nil {
		t.Fatalf("disabled: %v", err)
	}

	if len(fx.cli.lastSeenCalls) != 0 {
		t.Errorf("last_seen calls = %v, want none", fx.cli.lastSeenCalls)
	}
}

func TestHandleConnectionPostsOperationalNotice(t *testing.T) {
	fx := newFixture(nil)
	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}

	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	calls := fx.cli.creates()
	if len(calls) != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1 operational notice", len(calls))
	}
	if !contains(calls[0].req.Content, "conect") {
		t.Errorf("notice = %q, want a pt-BR connected message", calls[0].req.Content)
	}
	if fx.convs.lastJID == "" {
		t.Error("operational conversation was not resolved")
	}
}

func TestHandleConnectionThrottlesReconnectStorm(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()
	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}

	if err := fx.worker.HandleConnection(ctx, instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := fx.worker.HandleConnection(ctx, instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("second: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (second within 30s throttled)", n)
	}
}

func TestHandleConnectionQRAttachesImage(t *testing.T) {
	fx := newFixture(nil)
	// Minimal PNG header bytes stand in for the QR render.
	qr := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	notice := ConnectionNotice{Status: "pairing", QRImage: qr, PairingCode: "ABCD-1234"}

	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	atts := fx.cli.attachments()
	if len(atts) != 1 {
		t.Fatalf("attachment calls = %d, want 1 (QR image)", len(atts))
	}
	if !contains(atts[0].req.Content, "ABCD-1234") {
		t.Errorf("QR notice = %q, want the pairing code in text", atts[0].req.Content)
	}
}

func TestHandleMessageDecodesAppEnvelopePayload(t *testing.T) {
	raw := `{"from_jid":"5511999999999@s.whatsapp.net","chat_jid":"5511999999999@s.whatsapp.net",` +
		`"is_group":false,"message_id":"WA-MSG-9","timestamp":"2026-09-14T10:00:00Z",` +
		`"type":"text","text":"ping"}`
	var payload MessagePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode app payload: %v", err)
	}
	if payload.MessageID != "WA-MSG-9" || payload.Text != "ping" || payload.Type != "text" {
		t.Errorf("decoded = %+v, want the app event fields", payload)
	}
}

// TestHandleMessageDedupsViaPostgresCorrelation exercises the source_id
// dedup against the real correlation repository.
func TestHandleMessageDedupsViaPostgresCorrelation(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfgRepo, msgRepo := postgres.NewChatwootRepositories(pool)
	instanceID := uuid.New()
	if _, err := postgres.NewInstanceRepository(pool).Create(ctx, model.Instance{
		ID:     instanceID,
		Name:   "mirror-pg",
		Status: "disconnected",
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if _, err := cfgRepo.Put(ctx, model.ChatwootConfig{
		InstanceID: instanceID,
		Enabled:    true,
		URL:        "https://chatwoot.example",
		AccountID:  "1",
		Token:      "token",
		NameInbox:  "support",
		IgnoreJIDs: []string{},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cli := &fakeChatwootClient{inboxes: []client.Inbox{{ID: 5, Name: "support"}}}
	cts := &fakeContactResolver{contact: &contacts.Contact{ID: 11}}
	convs := &fakeConversationResolver{id: 22}
	worker := New(Deps{
		Configs:  cfgRepo,
		Messages: msgRepo,
		Global:   config.Chatwoot{Enabled: true},
		ClientFor: func(model.ChatwootConfig) ChatwootClient {
			return cli
		},
		ContactsFor: func(ChatwootClient, model.ChatwootConfig) ContactResolver {
			return cts
		},
		ConversationsFor: func(ChatwootClient, model.ChatwootConfig, int64) ConversationResolver {
			return convs
		},
	})

	msg := testMessagePayload()
	if err := worker.HandleMessage(ctx, instanceID, uuid.New(), msg); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := worker.HandleMessage(ctx, instanceID, uuid.New(), msg); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if n := len(cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (Postgres source_id dedup)", n)
	}
	if _, err := msgRepo.GetByWAKey(ctx, instanceID, "WA-MSG-1"); err != nil {
		t.Errorf("correlation missing: %v", err)
	}
}

// TestMirrorConsumerRedeliveryIsIdempotent runs the durable consumer against a
// real broker: the same message published twice mirrors once.
func TestMirrorConsumerRedeliveryIsIdempotent(t *testing.T) {
	url := natsURL(t)

	conn := dialNATS(t, url)
	defer conn.Close()
	js := jetStream(t, conn)
	stream, namespace := ensureTestStream(t, js)
	subject := namespace + ".message"

	fx := newFixture(nil)
	fx.worker.conn = conn
	fx.worker.stream = stream
	fx.worker.filter = namespace + ".>"
	instanceID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fx.worker.Run(ctx)
	}()
	waitForConsumer(t, js, stream)

	env := testMessageEnvelope(t, instanceID)
	publishEnvelope(t, js, subject, env)
	publishEnvelope(t, js, subject, env) // redelivery of the same event

	deadline := time.Now().Add(10 * time.Second)
	for len(fx.cli.creates()) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("consumer did not mirror the published message")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Give redelivery a chance to (incorrectly) duplicate.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (durable redelivery deduped)", n)
	}
}

// TestHandleMessagePutFailureKeepsSingleVisibleMessage pins the post-Create
// reconcile: when the correlation Put fails after a successful Chatwoot
// Create, the worker Acks (no error) instead of Naking into a visible
// duplicate, and the broker redelivery of the same event_id mirrors nothing.
func TestHandleMessagePutFailureKeepsSingleVisibleMessage(t *testing.T) {
	fx := newFixture(nil)
	fx.msgs.failPuts = 10
	fx.msgs.putErr = errors.New("postgres down")
	ctx := context.Background()
	instanceID := uuid.New()
	eventID := uuid.New()
	msg := testMessagePayload()

	if err := fx.worker.HandleMessage(ctx, instanceID, eventID, msg); err != nil {
		t.Fatalf("Put failure must Ack to avoid a duplicate, got error: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1 (single visible message)", n)
	}
	// Same broker redelivery (same event_id) must not create again.
	if err := fx.worker.HandleMessage(ctx, instanceID, eventID, msg); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls after redelivery = %d, want 1 (no visible duplicate)", n)
	}
}

// TestHandleMessagePutTransientFailureRecovers pins the retry inside the
// reconcile: a single Put blip succeeds on retry, so the correlation is
// present and the redelivery dedupes via the table.
func TestHandleMessagePutTransientFailureRecovers(t *testing.T) {
	fx := newFixture(nil)
	fx.msgs.failPuts = 1
	ctx := context.Background()
	instanceID := uuid.New()
	eventID := uuid.New()
	msg := testMessagePayload()

	if err := fx.worker.HandleMessage(ctx, instanceID, eventID, msg); err != nil {
		t.Fatalf("transient Put failure: %v", err)
	}
	if _, err := fx.msgs.GetByWAKey(ctx, instanceID, "WA-MSG-1"); err != nil {
		t.Fatalf("correlation missing after retry: %v", err)
	}
	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), msg); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (retry + PG dedup)", n)
	}
}

// TestHandleEditPutFailureKeepsSingleVisibleMessage pins the same reconcile
// for edits: Put failure after Create Acks, redelivery creates nothing.
func TestHandleEditPutFailureKeepsSingleVisibleMessage(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()

	if err := fx.worker.HandleMessage(ctx, instanceID, uuid.New(), testMessagePayload()); err != nil {
		t.Fatalf("original: %v", err)
	}
	fx.msgs.failPuts = 10
	edit := EditPayload{
		FromJID:   "5511999999999@s.whatsapp.net",
		ChatJID:   "5511999999999@s.whatsapp.net",
		MessageID: "WA-MSG-1",
		Text:      "editada com loja fechada",
		Timestamp: time.Now().UTC(),
	}
	eventID := uuid.New()
	if err := fx.worker.HandleEdit(ctx, instanceID, eventID, edit); err != nil {
		t.Fatalf("edit Put failure must Ack, got error: %v", err)
	}
	if n := len(fx.cli.creates()); n != 2 {
		t.Fatalf("CreateMessage calls = %d, want 2 (original + single edit)", n)
	}
	if err := fx.worker.HandleEdit(ctx, instanceID, eventID, edit); err != nil {
		t.Fatalf("edit redelivery: %v", err)
	}
	if n := len(fx.cli.creates()); n != 2 {
		t.Errorf("CreateMessage calls after redelivery = %d, want 2 (no duplicate edit)", n)
	}
}

// TestHandleConnectionThrottledRedeliveryDoesNotDuplicate pins that throttle
// suppresses sending, not dedup bookkeeping: a throttled event_id redelivered
// after the 30s window still posts nothing (alreadySeen), while a fresh
// event_id after the window posts again.
func TestHandleConnectionThrottledRedeliveryDoesNotDuplicate(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()
	instanceID := uuid.New()
	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}

	if err := fx.worker.HandleConnection(ctx, instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("first: %v", err)
	}
	throttledEvent := uuid.New()
	if err := fx.worker.HandleConnection(ctx, instanceID, throttledEvent, notice); err != nil {
		t.Fatalf("throttled: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1 (second throttled)", n)
	}
	// Let the 30s throttle window expire: the same throttled event_id must
	// still dedupe via seen, not post a duplicate notice.
	fx.worker.mu.Lock()
	if stamp, ok := fx.worker.notices[instanceID]; ok {
		stamp.at = stamp.at.Add(-31 * time.Second)
		fx.worker.notices[instanceID] = stamp
	}
	fx.worker.mu.Unlock()
	if err := fx.worker.HandleConnection(ctx, instanceID, throttledEvent, notice); err != nil {
		t.Fatalf("throttled redelivery after window: %v", err)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls after throttled redelivery = %d, want 1 (no duplicate notice)", n)
	}
	// A fresh event_id after the window is a new notice and posts again.
	if err := fx.worker.HandleConnection(ctx, instanceID, uuid.New(), notice); err != nil {
		t.Fatalf("fresh notice after window: %v", err)
	}
	if n := len(fx.cli.creates()); n != 2 {
		t.Errorf("CreateMessage calls after fresh notice = %d, want 2 (window expired)", n)
	}
}

// TestHandleMessageSkipsSearchLikePayload pins the searches spec gap: a
// search-like message (poll/search type with empty text and no media) never
// mirrors — it hits the unmappable-type skip. Contact-search probes
// (contacts.go FindContactByPhone/SearchContacts) are outbound Chatwoot HTTP
// that never enqueue a message event (see isStatusTraffic).
func TestHandleMessageSkipsSearchLikePayload(t *testing.T) {
	fx := newFixture(nil)
	ctx := context.Background()

	for _, msgType := range []string{"poll", "search"} {
		msg := testMessagePayload()
		msg.Type = msgType
		msg.Text = ""
		msg.Media = nil
		if err := fx.worker.HandleMessage(ctx, uuid.New(), uuid.New(), msg); err != nil {
			t.Fatalf("type %q must skip without error: %v", msgType, err)
		}
	}
	if n := len(fx.cli.creates()); n != 0 {
		t.Errorf("CreateMessage calls = %d, want 0 (search-like payloads ignored)", n)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
