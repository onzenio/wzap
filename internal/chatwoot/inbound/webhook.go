// Package inbound implements the Chatwoot to WhatsApp entry path (capability
// wzap-chatwoot-inbound): an open webhook turns attendant replies into queued
// outbound messages with retry and status, plus reverse sync and operational
// commands in the operational conversation.
package inbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/mapper"
	"wzap/internal/config"
	"wzap/internal/message"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

// Chatwoot webhook event names consumed here.
const (
	// EventMessageCreated is an attendant or contact message creation.
	EventMessageCreated = "message_created"
	// EventMessageUpdated covers edits, deletes and read markers.
	EventMessageUpdated = "message_updated"
)

// Attendant message types consumed here.
const (
	// MessageTypeOutgoing is an attendant reply to WhatsApp.
	MessageTypeOutgoing = "outgoing"
	// MessageTypeIncoming is a contact message mirrored from WhatsApp.
	MessageTypeIncoming = "incoming"
	// MessageTypeActivity is a system activity line, never sent to WhatsApp.
	MessageTypeActivity = "activity"
	// MessageTypeTemplate is a template message, sent without signature.
	MessageTypeTemplate = "template"
)

// Sender types consumed here.
const (
	// SenderTypeUser is a human attendant.
	SenderTypeUser = "user"
	// SenderTypeBot is an automated bot sender, discarded to avoid loops.
	SenderTypeBot = "agent_bot"
)

// OperationalContactIdentifier is the Chatwoot contact identifier of the
// operational conversation (connection notices, QR, status). It mirrors
// mirror.OperationalContactIdentifier for Evolution parity; operators
// override it with WZAP_CHATWOOT_BOT_CONTACT. Duplicated to avoid an
// inbound->mirror import for a single constant.
const OperationalContactIdentifier = "123456"

// sourceEchoPrefix namespaces mirrored Chatwoot messages back to WhatsApp.
// A webhook whose source_id carries it is our own echo and is discarded.
const sourceEchoPrefix = "WAID:"

// mediaDirectionOutbound labels media downloaded from Chatwoot for a send.
const mediaDirectionOutbound = "outbound"

// Payload is the Chatwoot agent-bot webhook event subset consumed here. Only
// the fields needed for filtering, signing, attachments, quoting, reverse
// delete, templates, read markers and operational commands are decoded;
// unknown fields are ignored.
type Payload struct {
	Event        string        `json:"event"`
	Message      *Message      `json:"message"`
	Conversation *Conversation `json:"conversation"`
}

// Message is a Chatwoot message in the webhook.
type Message struct {
	ID                int64          `json:"id"`
	Content           string         `json:"content"`
	MessageType       string         `json:"message_type"`
	Private           bool           `json:"private"`
	SourceID          string         `json:"source_id"`
	Sender            *Sender        `json:"sender"`
	Attachments       []Attachment   `json:"attachments"`
	InReplyTo         *int64         `json:"in_reply_to"`
	ContentAttributes map[string]any `json:"content_attributes"`
	ConversationID    int64          `json:"conversation_id"`
	// Deleted marks a message_updated carrying a reverse delete.
	Deleted bool `json:"deleted"`
}

// Sender is the author of a Chatwoot message.
type Sender struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Attachment is a Chatwoot message attachment with its download URL.
type Attachment struct {
	ID       int64  `json:"id"`
	FileType string `json:"file_type"`
	DataURL  string `json:"data_url"`
	FileName string `json:"file_name,omitempty"`
}

// Conversation carries the routing context of a webhook message.
type Conversation struct {
	ID           int64             `json:"id"`
	ContactInbox *ContactInbox     `json:"contact_inbox"`
	Meta         *ConversationMeta `json:"meta"`
}

// ContactInbox identifies the contact side of a conversation.
type ContactInbox struct {
	SourceID string `json:"source_id"`
}

// ConversationMeta carries the contact projection of a conversation.
type ConversationMeta struct {
	Sender *ContactSender `json:"sender"`
}

// ContactSender is the contact behind a conversation.
type ContactSender struct {
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Name        string `json:"name,omitempty"`
}

// Enqueuer queues attendant replies through the outbound outbox.
type Enqueuer interface {
	Enqueue(ctx context.Context, instanceID uuid.UUID, input message.EnqueueInput) (uuid.UUID, error)
}

// MediaSaver stores Chatwoot attachment bytes for the outbound sender.
type MediaSaver interface {
	Save(ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error)
}

// Correlations resolves Chatwoot messages back to WhatsApp keys for quoting,
// reverse delete and read markers.
type Correlations interface {
	GetByChatwootID(ctx context.Context, instanceID uuid.UUID, chatwootID int64) (*model.ChatwootMessage, error)
	LatestByConversation(ctx context.Context, instanceID uuid.UUID, conversationID int64) (*model.ChatwootMessage, error)
}

// Instances reads the instance an operational command reports on.
type Instances interface {
	Get(ctx context.Context, id uuid.UUID) (*model.Instance, error)
}

// Chats posts private failure notes and operational confirmations.
type Chats interface {
	CreateMessage(ctx context.Context, conversationID int64, content string, private bool) (int64, error)
}

// Downloader fetches attachment bytes from data_url. allowHost is the
// instance's configured Chatwoot url: private/loopback data_url hosts are
// accepted only when they match it (self-hosted Chatwoot), everything else
// follows the SSRF policy in ssrf.go.
type Downloader interface {
	Download(ctx context.Context, url, allowHost string) ([]byte, string, error)
}

// CacheClearer drops connector caches for the clearcache command.
type CacheClearer interface {
	Clear(instanceID uuid.UUID)
}

// Deps wires a Handler.
type Deps struct {
	Configs      storage.ChatwootConfigRepository
	Correlations Correlations
	Instances    Instances
	Enqueuer     Enqueuer
	Media        MediaSaver
	Sessions     session.Manager
	Chats        Chats
	Downloader   Downloader
	Cache        CacheClearer
	Global       config.Chatwoot
	Log          *slog.Logger
	// ClientFor builds the per-instance Chatwoot API for private notes and
	// operational confirmations. When set it wins over the static Chats
	// (tests use Chats directly); production wires client.New here so each
	// instance talks to its own Chatwoot account.
	ClientFor func(cfg model.ChatwootConfig) ChatwootAPI
}

// Handler turns Chatwoot webhook events into WhatsApp sends.
type Handler struct {
	configs      storage.ChatwootConfigRepository
	correlations Correlations
	instances    Instances
	enqueuer     Enqueuer
	media        MediaSaver
	sessions     session.Manager
	chats        Chats
	downloader   Downloader
	cache        CacheClearer
	global       config.Chatwoot
	log          *slog.Logger
	clientFor    func(cfg model.ChatwootConfig) ChatwootAPI
}

// New builds a Handler over deps. A nil log discards output.
func New(deps Deps) *Handler {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		configs:      deps.Configs,
		correlations: deps.Correlations,
		instances:    deps.Instances,
		enqueuer:     deps.Enqueuer,
		media:        deps.Media,
		sessions:     deps.Sessions,
		chats:        deps.Chats,
		downloader:   deps.Downloader,
		cache:        deps.Cache,
		global:       deps.Global,
		log:          log,
		clientFor:    deps.ClientFor,
	}
}

// Handle processes one webhook payload for instanceID and returns the HTTP
// status the open route must answer. Discards (no message, private,
// message_updated without delete, WAID: echo, bot, unknown events and types)
// answer 200 with no effect and are evaluated before anything else, so the
// operational conversation only routes qualifying message_created attendant
// replies; handled replies answer 200 after enqueueing; a globally disabled
// connector answers 400; internal failures answer 500. Send failures never
// fail the webhook: they post a private error note in the conversation and
// still answer 200.
func (h *Handler) Handle(ctx context.Context, instanceID uuid.UUID, payload Payload) (int, error) {
	if !h.global.Enabled {
		return 400, nil
	}
	cfg, err := h.configs.Get(ctx, instanceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return 200, nil
		}
		return 500, err
	}
	if cfg == nil || !cfg.Enabled {
		return 200, nil
	}
	if payload.Message == nil {
		return 200, nil
	}
	msg := payload.Message
	if msg.Private {
		return 200, nil
	}
	if msg.Sender != nil && msg.Sender.Type == SenderTypeBot {
		return 200, nil
	}
	if strings.HasPrefix(msg.SourceID, sourceEchoPrefix) {
		return 200, nil
	}
	if payload.Event == EventMessageUpdated {
		return h.handleMessageUpdated(ctx, instanceID, cfg, payload)
	}
	if payload.Event != EventMessageCreated {
		return 200, nil
	}
	if msg.MessageType != MessageTypeOutgoing && msg.MessageType != MessageTypeTemplate {
		return 200, nil
	}
	if h.isOperational(payload) {
		return h.handleOperational(ctx, instanceID, cfg, payload)
	}
	return h.handleOutgoing(ctx, instanceID, cfg, payload)
}

// handleMessageUpdated processes reverse deletes. Any other update (edit
// marker without delete, read marker handled opportunistically after sends)
// is discarded with 200.
func (h *Handler) handleMessageUpdated(ctx context.Context, instanceID uuid.UUID, cfg *model.ChatwootConfig, payload Payload) (int, error) {
	msg := payload.Message
	if !isDeleted(msg) {
		return 200, nil
	}
	if !h.global.MessageDelete {
		return 200, nil
	}
	corr, err := h.correlations.GetByChatwootID(ctx, instanceID, msg.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			h.log.Warn("skipping reverse delete without correlation", "instance_id", instanceID, "chatwoot_message_id", msg.ID)
			return 200, nil
		}
		return 500, err
	}
	sess, ok := h.sessions.Get(instanceID)
	if !ok {
		h.log.Warn("skipping reverse delete without session", "instance_id", instanceID)
		return 200, nil
	}
	if err := sess.DeleteMessage(ctx, corr.ContactSourceID, corr.WAKey); err != nil {
		h.log.Warn("reverse delete failed", "instance_id", instanceID, "error", err)
		h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao apagar no WhatsApp: %v", err))
		return 200, nil
	}
	return 200, nil
}

// handleOutgoing enqueues an attendant reply as text or media.
func (h *Handler) handleOutgoing(ctx context.Context, instanceID uuid.UUID, cfg *model.ChatwootConfig, payload Payload) (int, error) {
	msg := payload.Message
	recipient, err := h.recipient(ctx, instanceID, payload)
	if err != nil {
		h.log.Warn("recipient resolution failed", "instance_id", instanceID, "error", err)
		h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao resolver destinatário: %v", err))
		return 200, nil
	}
	if strings.TrimSpace(recipient) == "" {
		h.log.Warn("skipping outgoing without recipient", "instance_id", instanceID)
		h.postPrivateNote(ctx, cfg, conversationIDOf(payload), "Falha ao enviar: destinatário desconhecido.")
		return 200, nil
	}
	// Quoted replies resolve best-effort: without correlation the quote is
	// ignored and the send proceeds unquoted.
	var quotedID string
	if msg.InReplyTo != nil {
		corr, err := h.correlations.GetByChatwootID(ctx, instanceID, *msg.InReplyTo)
		if err != nil {
			if !errors.Is(err, storage.ErrNotFound) {
				return 500, err
			}
		} else if corr != nil {
			quotedID = corr.WAKey
		}
	}
	text := h.signedText(cfg, msg)
	if len(msg.Attachments) > 0 {
		enqueued := 0
		for i, att := range msg.Attachments {
			if strings.TrimSpace(att.DataURL) == "" {
				continue
			}
			data, mime, err := h.downloader.Download(ctx, att.DataURL, cfg.URL)
			if err != nil {
				h.log.Warn("attachment download failed", "instance_id", instanceID, "error", err)
				h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao baixar anexo: %v", err))
				continue
			}
			filename := strings.TrimSpace(att.FileName)
			if filename == "" {
				filename = "attachment"
			}
			stored, err := h.media.Save(ctx, instanceID, mediaDirectionOutbound, fmt.Sprintf("chatwoot-%d-%d", msg.ID, i), mime, filename, data)
			if err != nil {
				h.log.Warn("attachment store failed", "instance_id", instanceID, "error", err)
				h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao armazenar anexo: %v", err))
				continue
			}
			if _, err := h.enqueuer.Enqueue(ctx, instanceID, message.EnqueueInput{
				Type:     message.TypeMedia,
				To:       recipient,
				Caption:  text,
				Filename: stored.Filename,
				PTT:      false,
				MediaID:  &stored.ID,
				QuotedID: quotedID,
			}); err != nil {
				h.log.Warn("media enqueue failed", "instance_id", instanceID, "error", err)
				h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao enviar mídia ao WhatsApp: %v", err))
				continue
			}
			enqueued++
		}
		// When every attachment was skipped or failed but the message carries
		// text, fall back to a text send so the attendant content is not
		// silently lost.
		if enqueued == 0 && strings.TrimSpace(text) != "" {
			if _, err := h.enqueuer.Enqueue(ctx, instanceID, message.EnqueueInput{
				Type:     message.TypeText,
				To:       recipient,
				Text:     text,
				QuotedID: quotedID,
			}); err != nil {
				h.log.Warn("text fallback enqueue failed", "instance_id", instanceID, "error", err)
				h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao enviar ao WhatsApp: %v", err))
				return 200, nil
			}
			enqueued++
		}
		if enqueued > 0 {
			h.markReadBestEffort(ctx, instanceID, conversationIDOf(payload))
		}
		return 200, nil
	}
	if strings.TrimSpace(text) == "" {
		return 200, nil
	}
	if _, err := h.enqueuer.Enqueue(ctx, instanceID, message.EnqueueInput{
		Type:     message.TypeText,
		To:       recipient,
		Text:     text,
		QuotedID: quotedID,
	}); err != nil {
		h.log.Warn("text enqueue failed", "instance_id", instanceID, "error", err)
		h.postPrivateNote(ctx, cfg, conversationIDOf(payload), fmt.Sprintf("Falha ao enviar ao WhatsApp: %v", err))
		return 200, nil
	}
	h.markReadBestEffort(ctx, instanceID, conversationIDOf(payload))
	return 200, nil
}

// signedText renders the outbound text: templates go direct without
// signature, otherwise *name:* plus delimiter plus Chatwoot to WhatsApp
// markdown conversion when sign_msg is on.
func (h *Handler) signedText(cfg *model.ChatwootConfig, msg *Message) string {
	converted := mapper.MarkdownToWhatsApp(strings.TrimSpace(msg.Content))
	if msg.MessageType == MessageTypeTemplate {
		return converted
	}
	if cfg == nil || !cfg.SignMsg {
		return converted
	}
	if converted == "" {
		return ""
	}
	name := "Atendente"
	if msg.Sender != nil && strings.TrimSpace(msg.Sender.Name) != "" {
		name = strings.TrimSpace(msg.Sender.Name)
	}
	delim := cfg.SignDelimiter
	if delim == "" {
		delim = "\n"
	}
	return "*" + name + ":*" + delim + converted
}

// recipient resolves the WhatsApp destination: the latest correlation of the
// conversation first (the mirrored ContactSourceID is the chat JID for
// groups and the sender JID for direct chats, and the JID resolver passes
// JIDs through), falling back to the conversation contact phone/identifier.
func (h *Handler) recipient(ctx context.Context, instanceID uuid.UUID, payload Payload) (string, error) {
	convID := conversationIDOf(payload)
	if convID != 0 {
		if corr, err := h.correlations.LatestByConversation(ctx, instanceID, convID); err == nil && corr != nil && strings.TrimSpace(corr.ContactSourceID) != "" {
			return corr.ContactSourceID, nil
		} else if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return "", err
		}
	}
	if payload.Conversation != nil && payload.Conversation.Meta != nil && payload.Conversation.Meta.Sender != nil {
		sender := payload.Conversation.Meta.Sender
		if strings.TrimSpace(sender.PhoneNumber) != "" {
			return strings.TrimSpace(sender.PhoneNumber), nil
		}
		if strings.TrimSpace(sender.Identifier) != "" {
			return strings.TrimSpace(sender.Identifier), nil
		}
	}
	if payload.Conversation != nil && payload.Conversation.ContactInbox != nil {
		return strings.TrimSpace(payload.Conversation.ContactInbox.SourceID), nil
	}
	return "", nil
}

// isOperational reports whether payload belongs to the operational
// conversation, identified by the operational contact identifier.
func (h *Handler) isOperational(payload Payload) bool {
	want := h.operationalID()
	if payload.Conversation == nil {
		return false
	}
	if payload.Conversation.ContactInbox != nil && payload.Conversation.ContactInbox.SourceID == want {
		return true
	}
	if payload.Conversation.Meta != nil && payload.Conversation.Meta.Sender != nil && payload.Conversation.Meta.Sender.Identifier == want {
		return true
	}
	return false
}

// operationalID prefers the configured bot contact, falling back to parity.
func (h *Handler) operationalID() string {
	if strings.TrimSpace(h.global.BotContact) != "" {
		return strings.TrimSpace(h.global.BotContact)
	}
	return OperationalContactIdentifier
}

// handleOperational runs status/init/clearcache/disconnect in the
// operational conversation, confirming each one in-conversation.
func (h *Handler) handleOperational(ctx context.Context, instanceID uuid.UUID, cfg *model.ChatwootConfig, payload Payload) (int, error) {
	content := strings.TrimSpace(payload.Message.Content)
	lower := strings.ToLower(content)
	convID := conversationIDOf(payload)
	switch {
	case lower == "status":
		inst, err := h.instances.Get(ctx, instanceID)
		if err != nil {
			h.postOperationalConfirm(ctx, cfg, convID, "Falha ao obter estado da instância.")
			return 200, nil
		}
		reply := fmt.Sprintf("Instância %s: %s.", inst.Name, inst.Status)
		if strings.TrimSpace(inst.WhatsAppJID) != "" {
			reply += " JID: " + strings.TrimSpace(inst.WhatsAppJID)
		}
		h.postOperationalConfirm(ctx, cfg, convID, reply)
		return 200, nil
	case lower == "init" || strings.HasPrefix(lower, "init:"):
		sess, ok := h.sessions.Get(instanceID)
		if !ok {
			h.postOperationalConfirm(ctx, cfg, convID, "Sessão não encontrada para parear.")
			return 200, nil
		}
		number := ""
		if idx := strings.Index(content, ":"); idx >= 0 {
			number = strings.TrimSpace(content[idx+1:])
		}
		if number != "" {
			code, err := sess.PairPhone(ctx, number)
			if err != nil {
				h.postOperationalConfirm(ctx, cfg, convID, fmt.Sprintf("Falha ao parear %s: %v", number, err))
				return 200, nil
			}
			h.postOperationalConfirm(ctx, cfg, convID, fmt.Sprintf("Código de pareamento para %s: %s", number, code))
			return 200, nil
		}
		qr, _, err := sess.Connect(ctx)
		if err != nil {
			h.postOperationalConfirm(ctx, cfg, convID, fmt.Sprintf("Falha ao iniciar pareamento: %v", err))
			return 200, nil
		}
		reply := "Pareamento pendente. Escaneie o QR Code para conectar o WhatsApp."
		if strings.TrimSpace(qr) != "" {
			reply += " QR: " + strings.TrimSpace(qr)
		}
		h.postOperationalConfirm(ctx, cfg, convID, reply)
		return 200, nil
	case lower == "clearcache":
		if h.cache != nil {
			h.cache.Clear(instanceID)
		}
		h.postOperationalConfirm(ctx, cfg, convID, "Cache limpo.")
		return 200, nil
	case lower == "disconnect":
		sess, ok := h.sessions.Get(instanceID)
		if !ok {
			h.postOperationalConfirm(ctx, cfg, convID, "Sessão não encontrada para desconectar.")
			return 200, nil
		}
		if err := sess.Disconnect(ctx); err != nil {
			h.postOperationalConfirm(ctx, cfg, convID, fmt.Sprintf("Falha ao desconectar: %v", err))
			return 200, nil
		}
		h.postOperationalConfirm(ctx, cfg, convID, "WhatsApp desconectado pela operação.")
		return 200, nil
	default:
		h.postOperationalConfirm(ctx, cfg, convID, "Comandos: status, init[:number], clearcache, disconnect.")
		return 200, nil
	}
}

// markReadBestEffort marks the latest received message of the conversation
// as read when MESSAGE_READ is on. Without correlation or session it skips
// silently; failures only warn.
func (h *Handler) markReadBestEffort(ctx context.Context, instanceID uuid.UUID, conversationID int64) {
	if !h.global.MessageRead || conversationID == 0 {
		return
	}
	corr, err := h.correlations.LatestByConversation(ctx, instanceID, conversationID)
	if err != nil || corr == nil {
		return
	}
	sess, ok := h.sessions.Get(instanceID)
	if !ok {
		return
	}
	if err := sess.MarkRead(ctx, corr.ContactSourceID, "", corr.WAKey); err != nil {
		h.log.Warn("mark read failed", "instance_id", instanceID, "error", err)
	}
}

// chatsFor resolves the Chats for one request: the per-instance client wins
// when ClientFor is wired (production multi-account), otherwise the static
// Chats (tests).
func (h *Handler) chatsFor(cfg *model.ChatwootConfig) Chats {
	if h.clientFor != nil && cfg != nil {
		if api := h.clientFor(*cfg); api != nil {
			return NewAPIChats(api)
		}
	}
	return h.chats
}

// postPrivateNote posts a failure note as private; failures only warn.
func (h *Handler) postPrivateNote(ctx context.Context, cfg *model.ChatwootConfig, conversationID int64, content string) {
	chats := h.chatsFor(cfg)
	if chats == nil || conversationID == 0 || strings.TrimSpace(content) == "" {
		return
	}
	if _, err := chats.CreateMessage(ctx, conversationID, content, true); err != nil {
		h.log.Warn("private note failed", "conversation_id", conversationID, "error", err)
	}
}

// postOperationalConfirm confirms an operational command in-conversation.
func (h *Handler) postOperationalConfirm(ctx context.Context, cfg *model.ChatwootConfig, conversationID int64, content string) {
	chats := h.chatsFor(cfg)
	if chats == nil || conversationID == 0 {
		return
	}
	if _, err := chats.CreateMessage(ctx, conversationID, content, false); err != nil {
		h.log.Warn("operational confirm failed", "conversation_id", conversationID, "error", err)
	}
}

// conversationIDOf returns the conversation id of payload, preferring the
// message field.
func conversationIDOf(payload Payload) int64 {
	if payload.Message != nil && payload.Message.ConversationID != 0 {
		return payload.Message.ConversationID
	}
	if payload.Conversation != nil {
		return payload.Conversation.ID
	}
	return 0
}

// isDeleted reports whether an update carries a reverse delete.
func isDeleted(msg *Message) bool {
	if msg == nil {
		return false
	}
	if msg.Deleted {
		return true
	}
	if msg.ContentAttributes == nil {
		return false
	}
	deleted, _ := msg.ContentAttributes["deleted"].(bool)
	return deleted
}
