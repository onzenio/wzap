// Package mirror reflects WhatsApp inbound events into Chatwoot in real time.
//
// The worker is a READ-only JetStream consumer (durable "wzap-chatwoot"): it
// never publishes through events.Writer, so there is no second relay. Every
// handler is idempotent: redelivered events dedupe by event_id in memory and
// by source_id (WAID:<key>) in the correlation table, and Chatwoot slowness
// never blocks the session sink because consumption happens here, out of the
// session path.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"wzap/internal/chatwoot/client"
	"wzap/internal/chatwoot/contacts"
	"wzap/internal/chatwoot/conversations"
	"wzap/internal/chatwoot/mapper"
	"wzap/internal/config"
	"wzap/internal/events"
	"wzap/internal/media"
	"wzap/internal/model"
	"wzap/internal/storage"
)

const (
	// DurableConsumer is the JetStream durable name of the mirror. One
	// replica owns it; a second worker would share the durable and split
	// the stream, which the single-replica design forbids.
	DurableConsumer = "wzap-chatwoot"

	// OperationalContactIdentifier is the Chatwoot contact identifier of
	// the operational conversation (connection notices, QR, status). The
	// magic value is kept for Evolution parity; operators override it
	// with WZAP_CHATWOOT_BOT_CONTACT.
	OperationalContactIdentifier = "123456"

	// OperationalConversationKey scopes the operational conversation of
	// an instance inside the conversations resolver cache.
	OperationalConversationKey = "operational"

	// sourceIDPrefix namespaces every mirrored Chatwoot message back to
	// its WhatsApp key, so redeliveries dedupe and operators can trace
	// a Chatwoot message to WhatsApp.
	sourceIDPrefix = "WAID:"

	// reconnectThrottle is the minimum interval between two identical
	// connection notices of the same instance.
	reconnectThrottle = 30 * time.Second

	// seenTTL bounds the in-memory event_id dedup set; seenCap triggers
	// a sweep of expired entries.
	seenTTL = time.Hour
	seenCap = 10000
)

// The concrete dependencies satisfy the worker contracts; the assertions
// catch signature drift at build time.
var (
	_ ChatwootClient       = (*client.Client)(nil)
	_ ContactResolver      = (*contacts.Resolver)(nil)
	_ ConversationResolver = (*conversations.Resolver)(nil)
	_ MediaStore           = (*media.Storage)(nil)
)

// ChatwootClient is the Chatwoot surface the mirror needs. All methods are
// uniform (result, error); DeleteMessage and UpdateLastSeen return
// (struct{}, error).
type ChatwootClient interface {
	ListInboxes(ctx context.Context) ([]client.Inbox, error)
	CreateMessage(ctx context.Context, conversationID int64, req client.CreateMessageRequest) (*client.Message, error)
	CreateMessageWithAttachment(ctx context.Context, conversationID int64, req client.CreateMessageWithAttachmentRequest) (*client.Message, error)
	DeleteMessage(ctx context.Context, conversationID, messageID int64) (struct{}, error)
	UpdateLastSeen(ctx context.Context, conversationID int64) (struct{}, error)
}

// ContactResolver finds or creates the Chatwoot contact of a sender.
// *contacts.Resolver implements it.
type ContactResolver interface {
	Resolve(ctx context.Context, phone string, isGroup bool, name, avatar, jid string) (*contacts.Contact, error)
}

// ConversationResolver finds or opens the Chatwoot conversation of a sender.
// *conversations.Resolver implements it.
type ConversationResolver interface {
	Resolve(ctx context.Context, instanceID uuid.UUID, remoteJID string, contactID int64) (int64, error)
}

// MediaStore opens stored inbound media bytes. *media.Storage implements it.
type MediaStore interface {
	Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, *model.Media, error)
}

// MessagePayload is the JSON body of an inbound message event. The field
// names mirror the app producer so Run decodes its envelopes directly.
// Raw carries envelope.Event (the trimmed upstream event) for structured
// content (location, contact) that the flat fields cannot express.
type MessagePayload struct {
	FromJID      string          `json:"from_jid"`
	ChatJID      string          `json:"chat_jid"`
	IsGroup      bool            `json:"is_group"`
	MessageID    string          `json:"message_id"`
	Timestamp    time.Time       `json:"timestamp"`
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Media        *MediaRef       `json:"media,omitempty"`
	MediaOmitted *MediaOmission  `json:"media_omitted,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

// MediaRef references stored inbound media.
type MediaRef struct {
	MediaID   uuid.UUID `json:"media_id"`
	Mimetype  string    `json:"mimetype"`
	Filename  string    `json:"filename,omitempty"`
	Size      int64     `json:"size"`
	URL       string    `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// MediaOmission explains why inbound media was not stored.
type MediaOmission struct {
	Reason string `json:"reason"`
}

// EditPayload is the JSON body of an inbound message edit event.
type EditPayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Text      string    `json:"text,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// DeletePayload is the JSON body of an inbound message delete event.
type DeletePayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Timestamp time.Time `json:"timestamp"`
}

// ReadPayload is the JSON body of an inbound receipt event as the mirror
// consumes it: only read/played statuses reach last_seen.
type ReadPayload struct {
	MessageIDs []string  `json:"message_ids"`
	Status     string    `json:"status"`
	ChatJID    string    `json:"chat_jid,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// ConnectionNotice is the JSON body of a connection event plus the optional
// pairing material: the QR render and the pairing code travel out of band
// (the connection envelope carries neither), so direct callers attach them
// here and the consumer path sends the text notice alone.
type ConnectionNotice struct {
	Status      string `json:"status"`
	WhatsAppJID string `json:"whatsapp_jid,omitempty"`
	Reason      string `json:"reason,omitempty"`
	QRImage     []byte `json:"-"`
	PairingCode string `json:"-"`
}

// Deps wires a Worker. ClientFor, ContactsFor and ConversationsFor build the
// per-instance Chatwoot stack from the instance connector configuration;
// production passes client.New, contacts.New and conversations.New.
type Deps struct {
	Conn             *nats.Conn
	Stream           string
	Configs          storage.ChatwootConfigRepository
	Messages         storage.ChatwootMessageRepository
	Media            MediaStore
	Global           config.Chatwoot
	ClientFor        func(cfg model.ChatwootConfig) ChatwootClient
	ContactsFor      func(cli ChatwootClient, cfg model.ChatwootConfig) ContactResolver
	ConversationsFor func(cli ChatwootClient, cfg model.ChatwootConfig, inboxID int64) ConversationResolver
	Log              *slog.Logger
}

// Worker mirrors inbound events into Chatwoot. It is safe for concurrent use;
// per-sender serialization stays inside the conversations resolver.
type Worker struct {
	conn             *nats.Conn
	stream           string
	filter           string
	configs          storage.ChatwootConfigRepository
	messages         storage.ChatwootMessageRepository
	media            MediaStore
	global           config.Chatwoot
	clientFor        func(cfg model.ChatwootConfig) ChatwootClient
	contactsFor      func(cli ChatwootClient, cfg model.ChatwootConfig) ContactResolver
	conversationsFor func(cli ChatwootClient, cfg model.ChatwootConfig, inboxID int64) ConversationResolver
	log              *slog.Logger

	mu       sync.Mutex
	runtimes map[uuid.UUID]*instanceRuntime
	seen     map[uuid.UUID]time.Time
	notices  map[uuid.UUID]noticeStamp
}

// instanceRuntime caches the resolved Chatwoot stack of one instance.
type instanceRuntime struct {
	cfg   model.ChatwootConfig
	cli   ChatwootClient
	cts   ContactResolver
	convs ConversationResolver
	inbox int64
}

// noticeStamp records the last operational notice of an instance for the
// reconnect throttle.
type noticeStamp struct {
	status string
	at     time.Time
}

// New builds a Worker over deps. A nil log discards output.
func New(deps Deps) *Worker {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Worker{
		conn:             deps.Conn,
		stream:           deps.Stream,
		filter:           "wzap.>",
		configs:          deps.Configs,
		messages:         deps.Messages,
		media:            deps.Media,
		global:           deps.Global,
		clientFor:        deps.ClientFor,
		contactsFor:      deps.ContactsFor,
		conversationsFor: deps.ConversationsFor,
		log:              log,
		runtimes:         make(map[uuid.UUID]*instanceRuntime),
		seen:             make(map[uuid.UUID]time.Time),
		notices:          make(map[uuid.UUID]noticeStamp),
	}
}

// HandleMessage mirrors one inbound WhatsApp message into the sender
// conversation. Filtered senders skip silently; unmappable content and
// contact creation failures skip with a warn and never fail the worker.
// Persistent Chatwoot/storage failures return an error so the broker
// redelivers.
func (w *Worker) HandleMessage(ctx context.Context, instanceID, eventID uuid.UUID, msg MessagePayload) error {
	log := w.log.With("instance_id", instanceID, "event_id", eventID, "wa_key", msg.MessageID)
	if !w.global.Enabled {
		return nil
	}
	cfg, ok, err := w.connectorConfig(ctx, instanceID)
	if err != nil || !ok {
		return err
	}
	if ignoredJID(cfg, msg.FromJID, msg.ChatJID) || isStatusTraffic(msg.FromJID, msg.ChatJID) {
		log.Debug("skipping filtered sender")
		return nil
	}
	if w.alreadySeen(eventID) {
		return nil
	}
	rt, skip, err := w.runtimeFor(ctx, cfg)
	if err != nil || skip {
		return err
	}
	contact, err := w.resolveContact(ctx, rt, msg.FromJID, msg.ChatJID, msg.IsGroup)
	if err != nil || contact == nil {
		// resolveContact already warned: creation failures skip the
		// message, never the worker.
		return err
	}
	conversationID, err := rt.convs.Resolve(ctx, instanceID, msg.ChatJID, contact.ID)
	if err != nil {
		log.Warn("conversation resolution failed", "error", err)
		return err
	}
	if _, err := w.messages.GetByWAKey(ctx, instanceID, msg.MessageID); err == nil {
		w.markSeen(eventID)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}

	content := w.messageContent(msg)
	if content == "" && msg.Media == nil {
		log.Warn("skipping message with unmappable type", "type", msg.Type)
		return nil
	}

	var mirrored *client.Message
	if msg.Media != nil {
		mirrored, err = w.mirrorAttachment(ctx, log, rt, conversationID, msg, content)
	} else {
		mirrored, err = rt.cli.CreateMessage(ctx, conversationID, client.CreateMessageRequest{
			Content:           content,
			MessageType:       client.MessageTypeIncoming,
			ContentAttributes: messageAttributes(msg),
			SourceID:          waSourceID(msg.MessageID),
		})
	}
	if err != nil {
		log.Warn("chatwoot message creation failed", "error", err)
		return err
	}
	corr := model.ChatwootMessage{
		InstanceID:        instanceID,
		WAKey:             msg.MessageID,
		ChatwootMessageID: mirrored.ID,
		ConversationID:    conversationID,
		InboxID:           rt.inbox,
		ContactSourceID:   contactSource(msg),
	}
	// storeCorrelation reconciles the post-Create window so a Put failure
	// never Naks into a visible duplicate on redelivery.
	if err := w.storeCorrelation(ctx, log, rt.cli, corr); err != nil {
		log.Warn("correlation store failed", "error", err)
		return err
	}
	w.markSeen(eventID)
	return nil
}

// HandleEdit mirrors a message edit as a new message linked to the original
// via source_reply_id and marked "(editada)". Without a local correlation
// the edit has no anchor and is skipped with a warn.
func (w *Worker) HandleEdit(ctx context.Context, instanceID, eventID uuid.UUID, edit EditPayload) error {
	log := w.log.With("instance_id", instanceID, "event_id", eventID, "wa_key", edit.MessageID)
	if !w.global.Enabled {
		return nil
	}
	cfg, ok, err := w.connectorConfig(ctx, instanceID)
	if err != nil || !ok {
		return err
	}
	if ignoredJID(cfg, edit.FromJID, edit.ChatJID) || isStatusTraffic(edit.FromJID, edit.ChatJID) {
		log.Debug("skipping filtered sender")
		return nil
	}
	if w.alreadySeen(eventID) {
		return nil
	}
	editKey := edit.MessageID + "/edit/" + eventID.String()
	if _, err := w.messages.GetByWAKey(ctx, instanceID, editKey); err == nil {
		w.markSeen(eventID)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	original, err := w.messages.GetByWAKey(ctx, instanceID, edit.MessageID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			log.Warn("skipping edit without mirrored original")
			return nil
		}
		return err
	}
	rt, skip, err := w.runtimeFor(ctx, cfg)
	if err != nil || skip {
		return err
	}

	text := mapper.MarkdownToChatwoot(mapper.Text(mapper.TextInput{Body: edit.Text}))
	if edit.IsGroup {
		text = mapper.GroupPrefix(senderPhone(edit.FromJID), "", text)
	}
	content := strings.TrimSpace(text) + "\n(editada)"
	if strings.TrimSpace(text) == "" {
		content = "(mensagem editada)"
	}
	mirrored, err := rt.cli.CreateMessage(ctx, original.ConversationID, client.CreateMessageRequest{
		Content:           content,
		MessageType:       client.MessageTypeIncoming,
		ContentAttributes: editAttributes(edit),
		SourceID:          waSourceID(editKey),
		SourceReplyID:     strconv.FormatInt(original.ChatwootMessageID, 10),
	})
	if err != nil {
		log.Warn("chatwoot edit creation failed", "error", err)
		return err
	}
	corr := model.ChatwootMessage{
		InstanceID:        instanceID,
		WAKey:             editKey,
		ChatwootMessageID: mirrored.ID,
		ConversationID:    original.ConversationID,
		InboxID:           rt.inbox,
		ContactSourceID:   original.ContactSourceID,
	}
	// storeCorrelation reconciles the post-Create window so a Put failure
	// never Naks into a visible duplicate on redelivery.
	if err := w.storeCorrelation(ctx, log, rt.cli, corr); err != nil {
		log.Warn("correlation store failed", "error", err)
		return err
	}
	w.markSeen(eventID)
	return nil
}

// HandleDelete removes the Chatwoot mirror of a revoked message. The sync is
// gated by the global message-delete flag; without a local correlation there
// is nothing to remove and the event is skipped with a warn.
func (w *Worker) HandleDelete(ctx context.Context, instanceID, eventID uuid.UUID, del DeletePayload) error {
	log := w.log.With("instance_id", instanceID, "event_id", eventID, "wa_key", del.MessageID)
	if !w.global.Enabled || !w.global.MessageDelete {
		return nil
	}
	cfg, ok, err := w.connectorConfig(ctx, instanceID)
	if err != nil || !ok {
		return err
	}
	if ignoredJID(cfg, del.FromJID, del.ChatJID) || isStatusTraffic(del.FromJID, del.ChatJID) {
		log.Debug("skipping filtered sender")
		return nil
	}
	if w.alreadySeen(eventID) {
		return nil
	}
	original, err := w.messages.GetByWAKey(ctx, instanceID, del.MessageID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			log.Warn("skipping delete without mirrored original")
			return nil
		}
		return err
	}
	rt, skip, err := w.runtimeFor(ctx, cfg)
	if err != nil || skip {
		return err
	}
	if _, err := rt.cli.DeleteMessage(ctx, original.ConversationID, original.ChatwootMessageID); err != nil {
		log.Warn("chatwoot message deletion failed", "error", err)
		return err
	}
	w.markSeen(eventID)
	return nil
}

// HandleRead projects a read receipt into the conversation last_seen. Only
// read/played statuses qualify; the sync is gated by the global
// message-read flag.
func (w *Worker) HandleRead(ctx context.Context, instanceID, eventID uuid.UUID, read ReadPayload) error {
	log := w.log.With("instance_id", instanceID, "event_id", eventID)
	if !w.global.Enabled || !w.global.MessageRead {
		return nil
	}
	if read.Status != "read" && read.Status != "played" {
		return nil
	}
	cfg, ok, err := w.connectorConfig(ctx, instanceID)
	if err != nil || !ok {
		return err
	}
	if ignoredJID(cfg, read.ChatJID, read.ChatJID) || isStatusTraffic(read.ChatJID, read.ChatJID) {
		log.Debug("skipping filtered sender")
		return nil
	}
	if w.alreadySeen(eventID) {
		return nil
	}
	rt, skip, err := w.runtimeFor(ctx, cfg)
	if err != nil || skip {
		return err
	}
	contact, err := w.resolveContact(ctx, rt, read.ChatJID, read.ChatJID, isGroupJID(read.ChatJID))
	if err != nil || contact == nil {
		return err
	}
	conversationID, err := rt.convs.Resolve(ctx, instanceID, read.ChatJID, contact.ID)
	if err != nil {
		log.Warn("conversation resolution failed", "error", err)
		return err
	}
	if _, err := rt.cli.UpdateLastSeen(ctx, conversationID); err != nil {
		log.Warn("chatwoot last_seen update failed", "error", err)
		return err
	}
	w.markSeen(eventID)
	return nil
}

// HandleConnection posts the connection transition to the operational
// conversation in pt-BR. Identical consecutive notices within 30s are
// throttled so a reconnect storm does not flood the operators.
//
// Operational notices dedupe by event_id in memory only (no PG correlation:
// they carry no WAKey for edits/deletes and are idempotent status lines).
// Memory-only is accepted for a single replica under at-least-once: a restart
// may repost one status line, which is operator-visible noise, never user data
// loss or a duplicate chat message.
func (w *Worker) HandleConnection(ctx context.Context, instanceID, eventID uuid.UUID, notice ConnectionNotice) error {
	log := w.log.With("instance_id", instanceID, "event_id", eventID, "status", notice.Status)
	if !w.global.Enabled {
		return nil
	}
	cfg, ok, err := w.connectorConfig(ctx, instanceID)
	if err != nil || !ok {
		return err
	}
	if w.alreadySeen(eventID) {
		return nil
	}
	if w.throttled(instanceID, notice.Status) {
		log.Debug("throttling repeated connection notice")
		// Throttle suppresses sending, not dedup bookkeeping: the throttled
		// event_id must still be marked seen, or its redelivery after the
		// 30s window would post a duplicate notice.
		w.markSeen(eventID)
		return nil
	}
	rt, skip, err := w.runtimeFor(ctx, cfg)
	if err != nil || skip {
		return err
	}
	contactID := w.operationalContactID()
	contact, cerr := rt.cts.Resolve(ctx, contactID, false, "Operacional", "", contactID)
	if cerr != nil || contact == nil {
		log.Warn("operational contact resolution failed, skipping notice", "error", cerr)
		return nil
	}
	conversationID, err := rt.convs.Resolve(ctx, instanceID, OperationalConversationKey, contact.ID)
	if err != nil {
		log.Warn("operational conversation resolution failed", "error", err)
		return err
	}

	content := connectionText(notice)
	sourceID := waSourceID("connection/" + eventID.String())
	if len(notice.QRImage) > 0 {
		_, err = rt.cli.CreateMessageWithAttachment(ctx, conversationID, client.CreateMessageWithAttachmentRequest{
			Content:           content,
			MessageType:       client.MessageTypeIncoming,
			ContentAttributes: connectionAttributes(notice),
			SourceID:          sourceID,
			FileName:          "qrcode.png",
			ContentType:       "image/png",
			File:              notice.QRImage,
		})
	} else {
		_, err = rt.cli.CreateMessage(ctx, conversationID, client.CreateMessageRequest{
			Content:           content,
			MessageType:       client.MessageTypeIncoming,
			ContentAttributes: connectionAttributes(notice),
			SourceID:          sourceID,
		})
	}
	if err != nil {
		log.Warn("operational notice creation failed", "error", err)
		return err
	}
	w.stampNotice(instanceID, notice.Status)
	w.markSeen(eventID)
	return nil
}

// Run consumes the instance subjects through the durable consumer until ctx
// is done. It is READ-only: events are acknowledged after handling, never
// republished. A disabled connector or a missing connection returns without
// consuming; a broker outage retries until the context ends.
func (w *Worker) Run(ctx context.Context) {
	if !w.global.Enabled {
		w.log.Info("chatwoot mirror disabled, consumer not started")
		return
	}
	if w.conn == nil {
		w.log.Warn("chatwoot mirror has no NATS connection, consumer not started")
		return
	}
	stream := w.stream
	if stream == "" {
		stream = "WZAP"
	}
	for {
		if ctx.Err() != nil {
			return
		}
		js, err := w.conn.JetStream()
		if err != nil {
			w.log.Warn("chatwoot mirror jetstream unavailable, retrying", "error", err)
			if sleepContext(ctx, 5*time.Second) != nil {
				return
			}
			continue
		}
		if _, err := js.StreamInfo(stream, nats.Context(ctx)); err != nil {
			// The relay owns the stream: wait for it instead of creating a
			// competing one.
			w.log.Warn("chatwoot mirror stream not ready, retrying", "stream", stream, "error", err)
			if sleepContext(ctx, 5*time.Second) != nil {
				return
			}
			continue
		}
		sub, err := js.Subscribe(w.filter, w.handleNATSMessage,
			nats.Durable(DurableConsumer),
			nats.ManualAck(),
			nats.AckWait(30*time.Second),
			nats.MaxDeliver(10),
		)
		if err != nil {
			w.log.Warn("chatwoot mirror subscribe failed, retrying", "error", err)
			if sleepContext(ctx, 5*time.Second) != nil {
				return
			}
			continue
		}
		w.log.Info("chatwoot mirror consuming", "durable", DurableConsumer, "stream", stream)
		<-ctx.Done()
		_ = sub.Drain()
		return
	}
}

// handleNATSMessage routes one broker message to its handler. Handler errors
// Nak for redelivery; skips and successes Ack; undecodable poison Terms.
func (w *Worker) handleNATSMessage(msg *nats.Msg) {
	var env events.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		w.log.Warn("dropping undecodable event", "subject", msg.Subject, "error", err)
		_ = msg.Term()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	switch env.Type {
	case "message":
		var payload MessagePayload
		if derr := json.Unmarshal(env.Payload, &payload); derr != nil {
			w.log.Warn("dropping undecodable message payload", "event_id", env.EventID, "error", derr)
			_ = msg.Term()
			return
		}
		payload.Raw = env.Event
		err = w.HandleMessage(ctx, env.InstanceID, env.EventID, payload)
	case "message.edit":
		var payload EditPayload
		if derr := json.Unmarshal(env.Payload, &payload); derr != nil {
			w.log.Warn("dropping undecodable edit payload", "event_id", env.EventID, "error", derr)
			_ = msg.Term()
			return
		}
		err = w.HandleEdit(ctx, env.InstanceID, env.EventID, payload)
	case "message.delete":
		var payload DeletePayload
		if derr := json.Unmarshal(env.Payload, &payload); derr != nil {
			w.log.Warn("dropping undecodable delete payload", "event_id", env.EventID, "error", derr)
			_ = msg.Term()
			return
		}
		err = w.HandleDelete(ctx, env.InstanceID, env.EventID, payload)
	case "receipt":
		var payload ReadPayload
		if derr := json.Unmarshal(env.Payload, &payload); derr != nil {
			w.log.Warn("dropping undecodable receipt payload", "event_id", env.EventID, "error", derr)
			_ = msg.Term()
			return
		}
		err = w.HandleRead(ctx, env.InstanceID, env.EventID, payload)
	case "connection":
		var payload ConnectionNotice
		if derr := json.Unmarshal(env.Payload, &payload); derr != nil {
			w.log.Warn("dropping undecodable connection payload", "event_id", env.EventID, "error", derr)
			_ = msg.Term()
			return
		}
		err = w.HandleConnection(ctx, env.InstanceID, env.EventID, payload)
	case "message.status":
		// Outbound delivery statuses feed other consumers; the Chatwoot
		// mirror has nothing to project from them.
		_ = msg.Ack()
		return
	default:
		w.log.Warn("dropping event with unknown type", "event_id", env.EventID, "type", env.Type)
		_ = msg.Ack()
		return
	}
	if err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// connectorConfig loads the instance connector configuration. A missing or
// disabled configuration skips the event without an error.
func (w *Worker) connectorConfig(ctx context.Context, instanceID uuid.UUID) (*model.ChatwootConfig, bool, error) {
	if w.configs == nil {
		return nil, false, nil
	}
	cfg, err := w.configs.Get(ctx, instanceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, false, nil
	}
	return cfg, true, nil
}

// runtimeFor returns the cached Chatwoot stack of the instance, rebuilding it
// when the connector configuration changed.
func (w *Worker) runtimeFor(ctx context.Context, cfg *model.ChatwootConfig) (*instanceRuntime, bool, error) {
	w.mu.Lock()
	if rt, ok := w.runtimes[cfg.InstanceID]; ok && sameConnector(rt.cfg, *cfg) {
		w.mu.Unlock()
		return rt, false, nil
	}
	w.mu.Unlock()

	if w.clientFor == nil || w.contactsFor == nil || w.conversationsFor == nil {
		return nil, false, fmt.Errorf("chatwoot mirror has no client factories")
	}
	cli := w.clientFor(*cfg)
	inbox, skip, err := w.resolveInbox(ctx, cli, cfg)
	if err != nil || skip {
		return nil, skip, err
	}
	rt := &instanceRuntime{
		cfg:   *cfg,
		cli:   cli,
		cts:   w.contactsFor(cli, *cfg),
		convs: w.conversationsFor(cli, *cfg, inbox),
		inbox: inbox,
	}
	w.mu.Lock()
	w.runtimes[cfg.InstanceID] = rt
	w.mu.Unlock()
	return rt, false, nil
}

// resolveInbox locates the instance inbox by name. An empty or unknown name
// skips the event: inbox provisioning belongs to the auto_create flow, not
// to the mirror.
func (w *Worker) resolveInbox(ctx context.Context, cli ChatwootClient, cfg *model.ChatwootConfig) (int64, bool, error) {
	name := strings.TrimSpace(cfg.NameInbox)
	if name == "" {
		w.log.Warn("skipping mirror without inbox name", "instance_id", cfg.InstanceID)
		return 0, true, nil
	}
	inboxes, err := cli.ListInboxes(ctx)
	if err != nil {
		w.log.Warn("inbox listing failed", "instance_id", cfg.InstanceID, "error", err)
		return 0, false, err
	}
	for _, inbox := range inboxes {
		if inbox.Name == name {
			return inbox.ID, false, nil
		}
	}
	w.log.Warn("skipping mirror without provisioned inbox", "instance_id", cfg.InstanceID, "inbox", name)
	return 0, true, nil
}

// resolveContact resolves the Chatwoot contact of a sender: groups by their
// own chat JID, individuals by the sender phone. A creation failure warns
// and returns nil without an error so the caller skips the message.
func (w *Worker) resolveContact(ctx context.Context, rt *instanceRuntime, senderJID, chatJID string, isGroup bool) (*contacts.Contact, error) {
	phone, jid := senderJID, senderJID
	if isGroup {
		phone, jid = "", chatJID
	}
	contact, err := rt.cts.Resolve(ctx, phone, isGroup, "", "", jid)
	if err != nil || contact == nil {
		w.log.Warn("contact resolution failed, skipping message mirror", "jid", jid, "error", err)
		return nil, nil
	}
	return contact, nil
}

// messageContent renders the display text of a message: structured content
// (location, contact) is extracted from the upstream raw event when the flat
// text is empty, markdown is converted to the Chatwoot form, and group
// messages carry the sender prefix.
func (w *Worker) messageContent(msg MessagePayload) string {
	text := mapper.Text(mapper.TextInput{Body: msg.Text})
	if text == "" && len(msg.Raw) > 0 {
		text = structuredText(msg.Type, msg.Raw)
	}
	content := mapper.MarkdownToChatwoot(text)
	if msg.IsGroup {
		content = mapper.GroupPrefix(senderPhone(msg.FromJID), "", content)
	}
	return content
}

// mirrorAttachment uploads stored inbound media with its caption. When the
// bytes are gone the text still mirrors, so an expired TTL never loses the
// conversation.
func (w *Worker) mirrorAttachment(ctx context.Context, log *slog.Logger, rt *instanceRuntime, conversationID int64, msg MessagePayload, content string) (*client.Message, error) {
	data, mime, name, err := w.openMedia(ctx, msg.Media)
	if err != nil {
		reason := "unavailable"
		if msg.MediaOmitted != nil && msg.MediaOmitted.Reason != "" {
			reason = msg.MediaOmitted.Reason
		}
		log.Warn("inbound media unavailable, mirroring text only", "reason", reason, "error", err)
		if strings.TrimSpace(content) == "" {
			content = "(mídia não espelhada: " + reason + ")"
		}
		return rt.cli.CreateMessage(ctx, conversationID, client.CreateMessageRequest{
			Content:           content,
			MessageType:       client.MessageTypeIncoming,
			ContentAttributes: messageAttributes(msg),
			SourceID:          waSourceID(msg.MessageID),
		})
	}
	if kind, asDocument := mapper.AttachmentKind(mime, filepath.Ext(name)); asDocument {
		log.Debug("mirroring attachment as document", "kind", kind, "mime", mime)
	} else {
		log.Debug("mirroring attachment", "kind", kind, "mime", mime)
	}
	return rt.cli.CreateMessageWithAttachment(ctx, conversationID, client.CreateMessageWithAttachmentRequest{
		Content:           content,
		MessageType:       client.MessageTypeIncoming,
		ContentAttributes: messageAttributes(msg),
		SourceID:          waSourceID(msg.MessageID),
		FileName:          name,
		ContentType:       mime,
		File:              data,
	})
}

// openMedia reads the stored bytes of ref with its metadata.
func (w *Worker) openMedia(ctx context.Context, ref *MediaRef) ([]byte, string, string, error) {
	if w.media == nil {
		return nil, "", "", fmt.Errorf("media storage unavailable")
	}
	reader, record, err := w.media.Open(ctx, ref.MediaID)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", "", fmt.Errorf("read media %s: %w", ref.MediaID, err)
	}
	mime := ref.Mimetype
	if record != nil {
		if record.Mimetype != "" {
			mime = record.Mimetype
		}
		if record.Filename != "" {
			ref = &MediaRef{MediaID: ref.MediaID, Mimetype: mime, Filename: record.Filename, Size: ref.Size}
		}
	}
	name := strings.TrimSpace(ref.Filename)
	if name == "" {
		name = "attachment"
	}
	return data, mime, name, nil
}

// operationalContactID prefers the configured bot contact, falling back to
// the parity identifier.
func (w *Worker) operationalContactID() string {
	if strings.TrimSpace(w.global.BotContact) != "" {
		return strings.TrimSpace(w.global.BotContact)
	}
	return OperationalContactIdentifier
}

// alreadySeen reports whether eventID was handled before.
func (w *Worker) alreadySeen(eventID uuid.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.seen[eventID]
	return ok
}

// markSeen records eventID as handled, sweeping expired entries past the cap.
func (w *Worker) markSeen(eventID uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen[eventID] = time.Now().UTC()
	if len(w.seen) < seenCap {
		return
	}
	cutoff := time.Now().UTC().Add(-seenTTL)
	for id, at := range w.seen {
		if at.Before(cutoff) {
			delete(w.seen, id)
		}
	}
}

// throttled reports whether an identical notice was posted for the instance
// within the reconnect throttle window.
func (w *Worker) throttled(instanceID uuid.UUID, status string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	stamp, ok := w.notices[instanceID]
	return ok && stamp.status == status && time.Since(stamp.at) < reconnectThrottle
}

// stampNotice records a posted operational notice for the throttle.
func (w *Worker) stampNotice(instanceID uuid.UUID, status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.notices[instanceID] = noticeStamp{status: status, at: time.Now().UTC()}
}

// storeCorrelation records the WA→Chatwoot correlation after a successful
// Create. It preserves exactly-once-visible on redelivery: Put is an upsert,
// so a Put failure after Create would otherwise Nak and recreate a second
// visible message on redelivery (the pre-create Get still misses).
//
// Reconcile policy (minimal, no Chatwoot-side source_id search in the pinned
// client):
//   - Get succeeds (row exists): another delivery won the race; our
//     just-created message is an orphan duplicate unless it is the row itself
//     (Put committed but the response was lost). Best-effort delete the orphan,
//     then Ack.
//   - Get reports NotFound: our message is visible but uncorrelated. Retry Put
//     once for a transient blip; if it still fails, Ack with an orphan warn
//     (edits/deletes for the key will skip) to avoid a visible duplicate.
//   - Get fails otherwise (DB down): return the Put error for Nak. The
//     redelivery pre-create Get fails before Create, so no duplicate is
//     possible.
//
// Residual rare window (accepted): a producer double-publish of the same WAKey
// with different event_ids after an orphan Ack (no row, seen is per event_id)
// would Create again. It needs a lost correlation plus a duplicate envelope,
// which the outbox relay (Nats-Msg-Id = event_id) already dedups; closing it
// needs a Chatwoot-side source_id lookup the pinned client lacks.
func (w *Worker) storeCorrelation(ctx context.Context, log *slog.Logger, cli ChatwootClient, msg model.ChatwootMessage) error {
	if _, err := w.messages.Put(ctx, msg); err == nil {
		return nil
	} else {
		putErr := err
		existing, gerr := w.messages.GetByWAKey(ctx, msg.InstanceID, msg.WAKey)
		if gerr == nil {
			if existing.ChatwootMessageID != msg.ChatwootMessageID {
				if _, derr := cli.DeleteMessage(ctx, msg.ConversationID, msg.ChatwootMessageID); derr != nil {
					log.Warn("duplicate orphan cleanup failed, keeping single retry guard", "error", derr)
				} else {
					log.Warn("duplicate orphan removed after correlation race")
				}
			}
			return nil
		}
		if !errors.Is(gerr, storage.ErrNotFound) {
			return putErr
		}
		if _, err := w.messages.Put(ctx, msg); err == nil {
			return nil
		} else {
			putErr = err
		}
		log.Warn("correlation store failed, keeping visible message without correlation", "error", putErr)
		return nil
	}
}

// sameConnector reports whether the cached stack still matches cfg.
func sameConnector(a, b model.ChatwootConfig) bool {
	return a.URL == b.URL && a.AccountID == b.AccountID && a.Token == b.Token &&
		a.NameInbox == b.NameInbox && a.ConversationPending == b.ConversationPending &&
		a.ReopenConversation == b.ReopenConversation && a.MergeBrazilContacts == b.MergeBrazilContacts
}

// ignoredJID reports whether from or chat is on the instance ignore list.
func ignoredJID(cfg *model.ChatwootConfig, from, chat string) bool {
	for _, jid := range cfg.IgnoreJIDs {
		if jid == "" {
			continue
		}
		if from == jid || chat == jid {
			return true
		}
	}
	return false
}

// isStatusTraffic reports status-like traffic that never mirrors: broadcast
// statuses and channel newsletters.
//
// Searches need no JID filter here: contact-search probes emit no message
// events in this pipeline. The session dispatch
// (internal/session/whatsmeow/events.go dispatch) only translates Message,
// Receipt, HistorySync and connection events into sink calls; the contact
// resolver searches (internal/chatwoot/contacts/contacts.go FindContactByPhone,
// SearchContacts) are outbound Chatwoot HTTP that never enqueue a message
// event, and the app sink (internal/app/inbound.go handleInbound) only enqueues
// from OnMessage. A search-like message that somehow arrived (e.g. a poll with
// empty text and no media) still never mirrors: it hits the unmappable-type
// skip in HandleMessage (warn+skip, no worker failure).
func isStatusTraffic(from, chat string) bool {
	for _, jid := range []string{from, chat} {
		if strings.HasSuffix(jid, "@broadcast") || strings.HasSuffix(jid, "@newsletter") {
			return true
		}
	}
	return false
}

// isGroupJID reports whether jid is a group chat.
func isGroupJID(jid string) bool {
	_, server, ok := strings.Cut(jid, "@")
	return ok && server == "g.us"
}

// senderPhone renders the user part of a sender JID for group prefixes.
func senderPhone(senderJID string) string {
	user, _, ok := strings.Cut(senderJID, "@")
	if !ok {
		return senderJID
	}
	return user
}

// contactSource records the correlation origin: the group chat for groups,
// the sender for direct messages.
func contactSource(msg MessagePayload) string {
	if msg.IsGroup {
		return msg.ChatJID
	}
	return msg.FromJID
}

// waSourceID namespaces a WhatsApp key as a Chatwoot source_id.
func waSourceID(key string) string {
	return sourceIDPrefix + key
}

// messageAttributes carries the WA correlation of a mirrored message.
func messageAttributes(msg MessagePayload) map[string]any {
	return map[string]any{
		"wa_message_id": msg.MessageID,
		"wa_chat_jid":   msg.ChatJID,
		"wa_from_jid":   msg.FromJID,
		"wa_type":       msg.Type,
	}
}

// editAttributes carries the WA correlation of a mirrored edit.
func editAttributes(edit EditPayload) map[string]any {
	return map[string]any{
		"wa_message_id": edit.MessageID,
		"wa_chat_jid":   edit.ChatJID,
		"wa_from_jid":   edit.FromJID,
		"edited":        true,
	}
}

// connectionAttributes carries the WA correlation of an operational notice.
func connectionAttributes(notice ConnectionNotice) map[string]any {
	return map[string]any{
		"wa_status": notice.Status,
	}
}

// connectionText renders the operational notice in pt-BR.
func connectionText(notice ConnectionNotice) string {
	var body strings.Builder
	switch notice.Status {
	case "connected":
		body.WriteString("✅ WhatsApp conectado")
		if notice.WhatsAppJID != "" {
			body.WriteString(" como " + notice.WhatsAppJID)
		}
		body.WriteString(".")
	case "pairing":
		body.WriteString("📱 Pareamento pendente. Escaneie o QR Code para conectar o WhatsApp.")
		if notice.PairingCode != "" {
			body.WriteString("\nCódigo de pareamento: " + notice.PairingCode)
		}
	case "disconnected":
		body.WriteString("⚠️ WhatsApp desconectado.")
		if notice.Reason != "" {
			body.WriteString(" Motivo: " + notice.Reason)
		}
		body.WriteString(" Tentando reconectar…")
	case "error":
		body.WriteString("❌ Erro na conexão do WhatsApp.")
		if notice.Reason != "" {
			body.WriteString(" Motivo: " + notice.Reason)
		}
	default:
		body.WriteString("ℹ️ Estado da conexão: " + notice.Status + ".")
		if notice.Reason != "" {
			body.WriteString(" " + notice.Reason)
		}
	}
	return body.String()
}

// structuredText extracts display text for structured types (location,
// contact) from the trimmed upstream event. The second return reports
// whether extraction succeeded.
func structuredText(msgType string, raw json.RawMessage) string {
	var env struct {
		Message map[string]any `json:"Message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Message == nil {
		return ""
	}
	switch msgType {
	case "location":
		lat, lok := numberField(env.Message, "locationMessage", "degreesLatitude")
		lng, lngok := numberField(env.Message, "locationMessage", "degreesLongitude")
		if !lok || !lngok {
			return ""
		}
		name, _ := stringField(env.Message, "locationMessage", "name")
		addr, _ := stringField(env.Message, "locationMessage", "address")
		return mapper.Location(lat, lng, name, addr)
	case "contact":
		name, _ := stringField(env.Message, "contactMessage", "displayName")
		vcard, _ := stringField(env.Message, "contactMessage", "vcard")
		return mapper.Contact(name, vcardPhones(vcard))
	}
	return ""
}

// numberField navigates two map levels for a numeric field.
func numberField(msg map[string]any, outer, inner string) (float64, bool) {
	nested, ok := msg[outer].(map[string]any)
	if !ok {
		return 0, false
	}
	for _, key := range []string{inner, toSnake(inner)} {
		if value, ok := nested[key].(float64); ok {
			return value, true
		}
	}
	return 0, false
}

// stringField navigates two map levels for a string field, reporting "" when
// absent or of another type.
func stringField(msg map[string]any, outer, inner string) (string, bool) {
	nested, ok := msg[outer].(map[string]any)
	if !ok {
		return "", false
	}
	for _, key := range []string{inner, toSnake(inner)} {
		if value, ok := nested[key].(string); ok {
			return value, true
		}
	}
	return "", false
}

// toSnake converts a lowerCamel field name to snake_case for proto JSON
// variants.
func toSnake(s string) string {
	var out strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r - 'A' + 'a')
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// vcardPhones extracts the phone numbers of a vCard.
func vcardPhones(vcard string) []string {
	var phones []string
	for _, line := range strings.Split(vcard, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(name), "TEL") && strings.TrimSpace(value) != "" {
			phones = append(phones, strings.TrimSpace(value))
		}
	}
	return phones
}

// sleepContext sleeps d, returning ctx.Err() when the context ends first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
