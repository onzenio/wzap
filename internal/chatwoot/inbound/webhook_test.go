package inbound

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/message"
	"wzap/internal/model"
	"wzap/internal/session/sessiontest"
	"wzap/internal/storage"
)

// fakeEnqueuer records Enqueue calls.
type fakeEnqueuer struct {
	fn     func(ctx context.Context, instanceID uuid.UUID, input message.EnqueueInput) (uuid.UUID, error)
	inputs []message.EnqueueInput
	err    error
}

func (f *fakeEnqueuer) Enqueue(ctx context.Context, instanceID uuid.UUID, input message.EnqueueInput) (uuid.UUID, error) {
	f.inputs = append(f.inputs, input)
	if f.fn != nil {
		return f.fn(ctx, instanceID, input)
	}
	if f.err != nil {
		return uuid.Nil, f.err
	}
	return uuid.New(), nil
}

// fakeMediaSaver records Save calls.
type fakeMediaSaver struct {
	fn    func(ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error)
	saves []savedMedia
	err   error
}

type savedMedia struct {
	direction string
	mimetype  string
	filename  string
	data      []byte
}

func (f *fakeMediaSaver) Save(ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error) {
	f.saves = append(f.saves, savedMedia{direction: direction, mimetype: mimetype, filename: filename, data: data})
	if f.fn != nil {
		return f.fn(ctx, instanceID, direction, messageID, mimetype, filename, data)
	}
	if f.err != nil {
		return nil, f.err
	}
	id := uuid.New()
	return &model.Media{ID: id, InstanceID: instanceID, Direction: direction, Mimetype: mimetype, Filename: filename, SizeBytes: int64(len(data))}, nil
}

// fakeConfigs returns a fixed connector config.
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

// fakeCorrelations resolves Chatwoot IDs to WA keys.
type fakeCorrelations struct {
	byChatwootID map[int64]*model.ChatwootMessage
	latest       *model.ChatwootMessage
	latestErr    error
	lookups      []int64
}

func (f *fakeCorrelations) GetByChatwootID(_ context.Context, _ uuid.UUID, id int64) (*model.ChatwootMessage, error) {
	f.lookups = append(f.lookups, id)
	if msg, ok := f.byChatwootID[id]; ok {
		return msg, nil
	}
	return nil, storage.ErrNotFound
}

func (f *fakeCorrelations) LatestByConversation(_ context.Context, _ uuid.UUID, _ int64) (*model.ChatwootMessage, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	if f.latest == nil {
		return nil, storage.ErrNotFound
	}
	return f.latest, nil
}

// fakeInstances returns a fixed instance.
type fakeInstances struct {
	inst *model.Instance
	err  error
}

func (f *fakeInstances) Get(_ context.Context, _ uuid.UUID) (*model.Instance, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.inst, nil
}

// fakeChats records Chatwoot client messages.
type fakeChats struct {
	creates []createdChatMessage
	fn      func(ctx context.Context, conversationID int64, content string, private bool) (int64, error)
}

type createdChatMessage struct {
	conversationID int64
	content        string
	private        bool
}

func (f *fakeChats) CreateMessage(ctx context.Context, conversationID int64, content string, private bool) (int64, error) {
	f.creates = append(f.creates, createdChatMessage{conversationID: conversationID, content: content, private: private})
	if f.fn != nil {
		return f.fn(ctx, conversationID, content, private)
	}
	return 999, nil
}

// fakeDownloader returns fixed bytes.
type fakeDownloader struct {
	data  []byte
	mime  string
	err   error
	calls []string
}

func (f *fakeDownloader) Download(_ context.Context, url string) ([]byte, string, error) {
	f.calls = append(f.calls, url)
	if f.err != nil {
		return nil, "", f.err
	}
	return f.data, f.mime, nil
}

// fakeCache records clears.
type fakeCache struct {
	clears []uuid.UUID
}

func (f *fakeCache) Clear(instanceID uuid.UUID) {
	f.clears = append(f.clears, instanceID)
}

type fixture struct {
	handler   *Handler
	instance  uuid.UUID
	enqueuer  *fakeEnqueuer
	media     *fakeMediaSaver
	configs   *fakeConfigs
	correls   *fakeCorrelations
	instances *fakeInstances
	sessions  *sessiontest.Fake
	chats     *fakeChats
	down      *fakeDownloader
	cache     *fakeCache
}

func newFixture(t *testing.T, cfg *model.ChatwootConfig, global config.Chatwoot) *fixture {
	t.Helper()
	instanceID := uuid.New()
	if cfg != nil {
		cfg.InstanceID = instanceID
	}
	sessions := sessiontest.New(nil)
	sess := sessiontest.NewSession(instanceID, nil)
	sess.SetStatus("connected")
	sessions.Put(instanceID, sess)
	fx := &fixture{
		instance:  instanceID,
		enqueuer:  &fakeEnqueuer{},
		media:     &fakeMediaSaver{},
		configs:   &fakeConfigs{cfg: cfg},
		correls:   &fakeCorrelations{byChatwootID: map[int64]*model.ChatwootMessage{}},
		instances: &fakeInstances{inst: &model.Instance{ID: instanceID, Name: "loja", Status: "connected"}},
		sessions:  sessions,
		chats:     &fakeChats{},
		down:      &fakeDownloader{data: []byte("fake-image-bytes"), mime: "image/jpeg"},
		cache:     &fakeCache{},
	}
	fx.handler = New(Deps{
		Configs:      fx.configs,
		Correlations: fx.correls,
		Instances:    fx.instances,
		Enqueuer:     fx.enqueuer,
		Media:        fx.media,
		Sessions:     fx.sessions,
		Chats:        fx.chats,
		Downloader:   fx.down,
		Cache:        fx.cache,
		Global:       global,
	})
	return fx
}

func enabledConnector() *model.ChatwootConfig {
	return &model.ChatwootConfig{
		Enabled:       true,
		URL:           "https://chatwoot.example.com",
		AccountID:     "1",
		Token:         "secret",
		NameInbox:     "loja",
		SignMsg:       true,
		SignDelimiter: "\n",
	}
}

func globalOn() config.Chatwoot {
	return config.Chatwoot{Enabled: true, MessageRead: true, MessageDelete: true}
}

func outgoingPayload(conversationID int64, content string) Payload {
	return Payload{
		Event: EventMessageCreated,
		Message: &Message{
			ID:             101,
			Content:        content,
			MessageType:    MessageTypeOutgoing,
			SourceID:       "chatwoot-101",
			ConversationID: conversationID,
			Sender:         &Sender{Name: "Ana", Type: SenderTypeUser},
		},
		Conversation: &Conversation{
			ID:           conversationID,
			ContactInbox: &ContactInbox{SourceID: "+5511999999999"},
			Meta:         &ConversationMeta{Sender: &ContactSender{PhoneNumber: "+5511999999999"}},
		},
	}
}

func TestHandleEchoWAIDDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(55, "hello")
	payload.Message.SourceID = "WAID:ABC123"

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for WAID echo", len(fx.enqueuer.inputs))
	}
}

func TestHandlePrivateDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(55, "internal note")
	payload.Message.Private = true

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for private", len(fx.enqueuer.inputs))
	}
}

func TestHandleMessageUpdatedWithoutDeleteDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(55, "edited?")
	payload.Event = EventMessageUpdated

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for message_updated without delete", len(fx.enqueuer.inputs))
	}
}

func TestHandleBotDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(55, "bot says hi")
	payload.Message.Sender.Type = SenderTypeBot

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for bot", len(fx.enqueuer.inputs))
	}
}

func TestHandleTextEnqueuesWithSignature(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	payload := outgoingPayload(77, "**oi**")

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(fx.enqueuer.inputs))
	}
	got := fx.enqueuer.inputs[0]
	if got.Type != message.TypeText {
		t.Errorf("Enqueue type = %q, want text", got.Type)
	}
	// Signature *Ana:* + delimiter + markdown converted (**oi** -> *oi*).
	if got.Text != "*Ana:*\n*oi*" {
		t.Errorf("Enqueue text = %q, want %q", got.Text, "*Ana:*\n*oi*")
	}
}

func TestHandleAttachmentDownloadsAndEnqueuesMedia(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	payload := outgoingPayload(78, "look")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "https://chatwoot.example.com/rails/photo.jpg", FileName: "photo.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.down.calls) != 1 {
		t.Fatalf("Download calls = %d, want 1", len(fx.down.calls))
	}
	if len(fx.media.saves) != 1 {
		t.Fatalf("Save calls = %d, want 1", len(fx.media.saves))
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 media", len(fx.enqueuer.inputs))
	}
	got := fx.enqueuer.inputs[0]
	if got.Type != message.TypeMedia {
		t.Fatalf("Enqueue type = %q, want media", got.Type)
	}
	if got.PTT {
		t.Error("Enqueue PTT = true, want false (audio as non-PTT audio)")
	}
	if got.MediaID == nil {
		t.Error("Enqueue MediaID is nil, want stored media")
	}
}

func TestHandleQuotedViaCorrelation(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	quotedID := int64(202)
	fx.correls.byChatwootID[quotedID] = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-ORIG-1", ChatwootMessageID: quotedID,
		ConversationID: 79, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload := outgoingPayload(79, "replying")
	inReply := quotedID
	payload.Message.InReplyTo = &inReply

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 for known quoted", len(fx.enqueuer.inputs))
	}
	if len(fx.correls.lookups) == 0 {
		t.Error("correlation lookup missing for quoted")
	}
}

func TestHandleQuotedWithoutCorrelationIgnores(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	unknown := int64(9999)
	payload := outgoingPayload(79, "replying unknown")
	payload.Message.InReplyTo = &unknown

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 (quoted ignored without failing)", len(fx.enqueuer.inputs))
	}
}

func TestHandleReverseDeleteGated(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	deletedID := int64(303)
	fx.correls.byChatwootID[deletedID] = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-DEL-1", ChatwootMessageID: deletedID,
		ConversationID: 80, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload := Payload{
		Event: EventMessageUpdated,
		Message: &Message{
			ID: deletedID, MessageType: MessageTypeOutgoing,
			ConversationID: 80, Deleted: true,
			ContentAttributes: map[string]any{"deleted": true},
		},
		Conversation: &Conversation{ID: 80},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	sess, _ := fx.sessions.Get(fx.instance)
	fake := sess.(*sessiontest.FakeSession)
	if len(fake.DeleteCalls()) != 1 {
		t.Fatalf("DeleteMessage calls = %d, want 1", len(fake.DeleteCalls()))
	}

	// Disabled gate skips the delete.
	fx2 := newFixture(t, enabledConnector(), config.Chatwoot{Enabled: true, MessageDelete: false})
	fx2.correls.byChatwootID[deletedID] = &model.ChatwootMessage{
		InstanceID: fx2.instance, WAKey: "WA-DEL-1", ChatwootMessageID: deletedID,
		ConversationID: 80, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload2 := Payload{
		Event: EventMessageUpdated,
		Message: &Message{
			ID: deletedID, MessageType: MessageTypeOutgoing,
			ConversationID: 80, Deleted: true,
			ContentAttributes: map[string]any{"deleted": true},
		},
		Conversation: &Conversation{ID: 80},
	}
	if status, err := fx2.handler.Handle(context.Background(), fx2.instance, payload2); err != nil || status != 200 {
		t.Fatalf("Handle disabled = (%d, %v), want (200, nil)", status, err)
	}
	sess2, _ := fx2.sessions.Get(fx2.instance)
	if len(sess2.(*sessiontest.FakeSession).DeleteCalls()) != 0 {
		t.Error("DeleteMessage called while MessageDelete disabled")
	}
}

func TestHandleTemplateWithoutSignature(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	payload := outgoingPayload(81, "**promo**")
	payload.Message.MessageType = MessageTypeTemplate

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(fx.enqueuer.inputs))
	}
	got := fx.enqueuer.inputs[0].Text
	if strings.Contains(got, "*Ana:*") {
		t.Errorf("template text = %q, want no signature", got)
	}
}

func TestHandleMessageReadMarksLastReceived(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latest = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-LAST-1", ChatwootMessageID: 404,
		ConversationID: 82, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload := outgoingPayload(82, "hello read")

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	sess, _ := fx.sessions.Get(fx.instance)
	if len(sess.(*sessiontest.FakeSession).MarkReadCalls()) == 0 {
		t.Error("MarkRead not called with MESSAGE_READ enabled")
	}
}

func TestHandleOperationalStatus(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "status")
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for operational command", len(fx.enqueuer.inputs))
	}
	if len(fx.chats.creates) != 1 {
		t.Fatalf("confirm messages = %d, want 1", len(fx.chats.creates))
	}
}

func TestHandleOperationalInitWithNumber(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "init:5511999999999")
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	sess, _ := fx.sessions.Get(fx.instance)
	if len(sess.(*sessiontest.FakeSession).PairPhoneCalls()) != 1 {
		t.Fatalf("PairPhone calls = %d, want 1", len(sess.(*sessiontest.FakeSession).PairPhoneCalls()))
	}
	if got := sess.(*sessiontest.FakeSession).PairPhoneCalls()[0].Number; got != "5511999999999" {
		t.Errorf("PairPhone number = %q, want 5511999999999", got)
	}
	if len(fx.chats.creates) != 1 {
		t.Fatalf("confirm messages = %d, want 1", len(fx.chats.creates))
	}
}

func TestHandleOperationalClearCache(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "clearcache")
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.cache.clears) != 1 {
		t.Fatalf("Clear calls = %d, want 1", len(fx.cache.clears))
	}
	if len(fx.chats.creates) != 1 {
		t.Fatalf("confirm messages = %d, want 1", len(fx.chats.creates))
	}
}

func TestHandleOperationalDisconnect(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "disconnect")
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	sess, _ := fx.sessions.Get(fx.instance)
	if sess.(*sessiontest.FakeSession).DisconnectCalls() != 1 {
		t.Error("Disconnect not called for operational disconnect")
	}
	if len(fx.chats.creates) != 1 {
		t.Fatalf("confirm messages = %d, want 1", len(fx.chats.creates))
	}
}

func TestHandleFailurePostsPrivateNote(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	fx.enqueuer.err = errors.New("boom")
	payload := outgoingPayload(83, "will fail")

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200 even on Enqueue failure", status)
	}
	if len(fx.chats.creates) != 1 {
		t.Fatalf("private notes = %d, want 1", len(fx.chats.creates))
	}
	if !fx.chats.creates[0].private {
		t.Error("failure note is not private")
	}
}

func TestHandleGlobalDisabledReturns400(t *testing.T) {
	fx := newFixture(t, enabledConnector(), config.Chatwoot{Enabled: false})
	payload := outgoingPayload(55, "hello")

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 400 {
		t.Fatalf("status = %d, want 400 when globally disabled", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 when disabled", len(fx.enqueuer.inputs))
	}
}
