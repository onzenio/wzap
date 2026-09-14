package inbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
	messageID string
	mimetype  string
	filename  string
	data      []byte
}

func (f *fakeMediaSaver) Save(ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error) {
	f.saves = append(f.saves, savedMedia{direction: direction, messageID: messageID, mimetype: mimetype, filename: filename, data: data})
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

func (f *fakeDownloader) Download(_ context.Context, url, _ string) ([]byte, string, error) {
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
	if got := fx.enqueuer.inputs[0].QuotedID; got != "WA-ORIG-1" {
		t.Errorf("Enqueue QuotedID = %q, want the correlated WA key WA-ORIG-1", got)
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
	if got := fx.enqueuer.inputs[0].QuotedID; got != "" {
		t.Errorf("Enqueue QuotedID = %q, want empty for unknown correlation", got)
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
	status, err := fx.handler.HandleCommand(context.Background(), fx.instance, "status", 90)
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
	status, err := fx.handler.HandleCommand(context.Background(), fx.instance, "init:5511999999999", 90)
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
	status, err := fx.handler.HandleCommand(context.Background(), fx.instance, "clearcache", 90)
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
	status, err := fx.handler.HandleCommand(context.Background(), fx.instance, "disconnect", 90)
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

func TestHandleAttachmentsAllEmptyFallsBackToText(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latest = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-LAST-1", ChatwootMessageID: 404,
		ConversationID: 84, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload := outgoingPayload(84, "texto importante")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "   ", FileName: "empty.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.down.calls) != 0 {
		t.Fatalf("Download calls = %d, want 0 for empty data_url", len(fx.down.calls))
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 text fallback", len(fx.enqueuer.inputs))
	}
	got := fx.enqueuer.inputs[0]
	if got.Type != message.TypeText {
		t.Errorf("Enqueue type = %q, want text fallback", got.Type)
	}
	if got.Text != "*Ana:*\ntexto importante" {
		t.Errorf("Enqueue text = %q, want the signed attendant text", got.Text)
	}
	sess, _ := fx.sessions.Get(fx.instance)
	if len(sess.(*sessiontest.FakeSession).MarkReadCalls()) == 0 {
		t.Error("MarkRead not called after the text fallback enqueue")
	}
}

func TestHandleAttachmentsAllFailedFallsBackToText(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	fx.down.err = errors.New("network down")
	payload := outgoingPayload(85, "texto importante")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "https://chatwoot.example.com/rails/a.jpg", FileName: "a.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 text fallback", len(fx.enqueuer.inputs))
	}
	if got := fx.enqueuer.inputs[0]; got.Type != message.TypeText {
		t.Errorf("Enqueue type = %q, want text fallback", got.Type)
	}
	if len(fx.chats.creates) == 0 {
		t.Error("expected a private note for the failed download")
	}
}

func TestHandleAttachmentsTotalFailureWithoutTextSkipsMarkRead(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latest = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-LAST-1", ChatwootMessageID: 404,
		ConversationID: 86, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	fx.down.err = errors.New("network down")
	payload := outgoingPayload(86, "   ")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "https://chatwoot.example.com/rails/a.jpg", FileName: "a.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 on total failure without text", len(fx.enqueuer.inputs))
	}
	sess, _ := fx.sessions.Get(fx.instance)
	if len(sess.(*sessiontest.FakeSession).MarkReadCalls()) != 0 {
		t.Error("MarkRead called without any enqueue, want no read marker on total failure")
	}
}

func TestHandleTwoAttachmentsUseDistinctMediaIDs(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	payload := outgoingPayload(87, "duas fotos")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "https://chatwoot.example.com/rails/a.jpg", FileName: "a.jpg"},
		{ID: 2, FileType: "image", DataURL: "https://chatwoot.example.com/rails/b.jpg", FileName: "b.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.media.saves) != 2 {
		t.Fatalf("Save calls = %d, want 2", len(fx.media.saves))
	}
	if fx.media.saves[0].messageID == fx.media.saves[1].messageID {
		t.Errorf("Save messageIDs collide = %q, want one unique id per attachment", fx.media.saves[0].messageID)
	}
	if len(fx.enqueuer.inputs) != 2 {
		t.Fatalf("Enqueue calls = %d, want 2 media", len(fx.enqueuer.inputs))
	}
	for i, got := range fx.enqueuer.inputs {
		if got.Type != message.TypeMedia {
			t.Errorf("Enqueue[%d] type = %q, want media", i, got.Type)
		}
		if got.MediaID == nil {
			t.Errorf("Enqueue[%d] MediaID is nil, want stored media", i)
		}
	}
}

func TestHandleQuotedMediaCarriesQuotedID(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.correls.latestErr = storage.ErrNotFound
	quotedID := int64(202)
	fx.correls.byChatwootID[quotedID] = &model.ChatwootMessage{
		InstanceID: fx.instance, WAKey: "WA-ORIG-1", ChatwootMessageID: quotedID,
		ConversationID: 88, ContactSourceID: "5511999999999@s.whatsapp.net",
	}
	payload := outgoingPayload(88, "olha")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "https://chatwoot.example.com/rails/photo.jpg", FileName: "photo.jpg"},
	}
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
		t.Fatalf("Enqueue calls = %d, want 1 media", len(fx.enqueuer.inputs))
	}
	got := fx.enqueuer.inputs[0]
	if got.Type != message.TypeMedia {
		t.Fatalf("Enqueue type = %q, want media", got.Type)
	}
	if got.QuotedID != "WA-ORIG-1" {
		t.Errorf("Enqueue QuotedID = %q, want the correlated WA key WA-ORIG-1", got.QuotedID)
	}
}

func TestHandleOperationalPrivateDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "status")
	payload.Message.Private = true
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.chats.creates) != 0 {
		t.Fatalf("confirm messages = %d, want 0 for a private note in the operational conversation", len(fx.chats.creates))
	}
	if len(fx.enqueuer.inputs) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(fx.enqueuer.inputs))
	}
}

func TestHandleOperationalMessageUpdatedDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "status")
	payload.Event = EventMessageUpdated
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.chats.creates) != 0 {
		t.Fatalf("confirm messages = %d, want 0 for message_updated in the operational conversation", len(fx.chats.creates))
	}
}

func TestHandleOperationalIncomingDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "status")
	payload.Message.MessageType = MessageTypeIncoming
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.chats.creates) != 0 {
		t.Fatalf("confirm messages = %d, want 0 for an incoming message in the operational conversation", len(fx.chats.creates))
	}
}

func TestHandleOperationalBotDiscards(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	payload := outgoingPayload(90, "status")
	payload.Message.Sender.Type = SenderTypeBot
	payload.Conversation.ContactInbox.SourceID = OperationalContactIdentifier
	payload.Conversation.Meta.Sender.Identifier = OperationalContactIdentifier

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(fx.chats.creates) != 0 {
		t.Fatalf("confirm messages = %d, want 0 for a bot message in the operational conversation", len(fx.chats.creates))
	}
}

// TestValidateAttachmentURLPolicy pins the SSRF allowlist: public URLs
// pass, metadata/link-local are always blocked, and private/loopback hosts
// pass only for the configured Chatwoot host (self-hosted case).
func TestValidateAttachmentURLPolicy(t *testing.T) {
	old := lookupIP
	defer func() { lookupIP = old }()
	lookupIP = func(host string) ([]net.IP, error) {
		switch host {
		case "public.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "internal.example":
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		default:
			return nil, errors.New("no such host")
		}
	}
	chatwoot := "https://chatwoot.example.com"
	cases := []struct {
		name   string
		url    string
		allow  string
		reject bool
	}{
		{"public https passes", "https://93.184.216.34/rails/a.jpg", chatwoot, false},
		{"public hostname passes", "https://public.example/rails/a.jpg", chatwoot, false},
		{"metadata IP rejected", "http://169.254.169.254/latest/meta-data", chatwoot, true},
		{"alibaba metadata rejected", "http://100.100.100.200/latest/meta-data", chatwoot, true},
		{"private non-chatwoot rejected", "https://192.168.1.10/rails/a.jpg", chatwoot, true},
		{"loopback non-chatwoot rejected", "http://127.0.0.1:8080/rails/a.jpg", chatwoot, true},
		{"private matching chatwoot host allowed", "http://10.0.0.5/rails/a.jpg", "http://10.0.0.5", false},
		{"loopback matching chatwoot host allowed", "http://127.0.0.1:8080/rails/a.jpg", "http://127.0.0.1:8080", false},
		{"metadata matching chatwoot host still rejected", "http://169.254.169.254/x", "http://169.254.169.254", true},
		{"ftp scheme rejected", "ftp://93.184.216.34/a.jpg", chatwoot, true},
		{"file scheme rejected", "file:///etc/passwd", chatwoot, true},
		{"private hostname rejected", "https://internal.example/a.jpg", chatwoot, true},
		{"private hostname allowed for self-host", "https://internal.example/a.jpg", "https://internal.example", false},
		{"unresolvable rejected", "https://ghost.example/a.jpg", chatwoot, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAttachmentURL(tc.url, tc.allow)
			if tc.reject && err == nil {
				t.Errorf("validateAttachmentURL(%q) = nil, want rejection", tc.url)
			}
			if !tc.reject && err != nil {
				t.Errorf("validateAttachmentURL(%q) = %v, want pass", tc.url, err)
			}
		})
	}
}

// TestHTTPDownloaderRedirectCap pins the redirect policy against a local
// server (loopback allowed as self-host): one hop passes, a four-hop chain
// stops at the cap, and loopback without a matching host never dials.
func TestHTTPDownloaderRedirectCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-bytes"))
	})
	mux.HandleFunc("/ok1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file", http.StatusFound)
	})
	// Four-hop chain r1 -> r2 -> r3 -> r4 -> file, over the redirect cap.
	for _, hop := range []struct{ from, to string }{
		{"/r1", "/r2"}, {"/r2", "/r3"}, {"/r3", "/r4"}, {"/r4", "/file"},
	} {
		to := hop.to
		mux.HandleFunc(hop.from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, to, http.StatusFound)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	downloader := NewHTTPDownloader(1 << 20)
	ctx := context.Background()

	data, mime, err := downloader.Download(ctx, srv.URL+"/ok1", srv.URL)
	if err != nil {
		t.Fatalf("single redirect Download: %v", err)
	}
	if string(data) != "fake-bytes" || mime != "image/jpeg" {
		t.Errorf("Download = (%q, %q), want the file bytes with its mime", data, mime)
	}

	if _, _, err := downloader.Download(ctx, srv.URL+"/r1", srv.URL); err == nil {
		t.Fatal("four-hop chain Download error = nil, want the redirect cap to stop it")
	} else if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("four-hop chain error = %q, want it to name the redirect cap", err.Error())
	}

	if _, _, err := downloader.Download(ctx, srv.URL+"/file", "https://chatwoot.example.com"); err == nil {
		t.Fatal("loopback without self-host match error = nil, want rejection before dialing")
	}
}

// TestHandleAttachmentSSRFRejectedKeepsWebhook200 pins that an SSRF
// rejection fails the attachment into a private note with a 200 answer: the
// webhook never breaks, and the attendant text still goes out as fallback.
func TestHandleAttachmentSSRFRejectedKeepsWebhook200(t *testing.T) {
	fx := newFixture(t, enabledConnector(), globalOn())
	fx.handler.downloader = NewHTTPDownloader(1 << 20)
	fx.correls.latestErr = storage.ErrNotFound
	payload := outgoingPayload(78, "look")
	payload.Message.Attachments = []Attachment{
		{ID: 1, FileType: "image", DataURL: "http://169.254.169.254/latest/meta-data", FileName: "meta.jpg"},
	}

	status, err := fx.handler.Handle(context.Background(), fx.instance, payload)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200 (SSRF rejection never breaks the webhook)", status)
	}
	if len(fx.chats.creates) == 0 {
		t.Fatal("private notes = 0, want the rejection note")
	}
	if !fx.chats.creates[0].private {
		t.Error("rejection note is not private")
	}
	if len(fx.enqueuer.inputs) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1 text fallback", len(fx.enqueuer.inputs))
	}
	if got := fx.enqueuer.inputs[0]; got.Type != message.TypeText {
		t.Errorf("Enqueue type = %q, want text fallback", got.Type)
	}
}
