package whatsmeow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"wzap/internal/session"
)

// dispatch translates a whatsmeow event into a session event. It is registered
// as the client event handler, so it may run concurrently and must never block
// on slow sinks.
func (s *instanceSession) dispatch(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		if e.Info.IsFromMe || e.Message == nil || s.sink == nil {
			return
		}
		// The translation prefers the alternative (LID) sender address the
		// pinned library exposes; without one it falls back to the primary
		// JID and warns, so LID-less peers stay visible in the logs.
		if e.Info.SenderAlt.IsEmpty() {
			s.log.Warn("message without alt sender address, using primary JID",
				"instance_id", s.instanceID, "chat", e.Info.Chat.String(), "message_id", e.Info.ID)
		}
		// The pinned library surfaces live edits and deletes as Message events
		// carrying a MESSAGE_EDIT/REVOKE protocol message (already unwrapped
		// from EditedMessage by UnwrapRaw), while history-sync edits arrive
		// with IsEdit set and the id rewritten to the original. Both fork to
		// the edit/delete sink before the plain message path.
		if target, ok := messageEditTarget(e); ok {
			s.sink.OnMessageEdit(context.Background(), editMessage(s.instanceID, e, target))
			return
		}
		// A delete-for-everyone never surfaces as a plain message: with the
		// original key it becomes a delete event, without one it is dropped
		// (there is nothing to mirror). The nil check matters because REVOKE
		// is the zero value of the protocol enum.
		if protoMsg := e.Message.GetProtocolMessage(); protoMsg != nil &&
			protoMsg.GetType() == waE2E.ProtocolMessage_REVOKE {
			if originalID, ok := messageDeleteTarget(e); ok {
				s.sink.OnMessageDelete(context.Background(), deleteMessage(s.instanceID, e, originalID))
			} else {
				s.log.Warn("dropping revoke without original message key",
					"instance_id", s.instanceID, "chat", e.Info.Chat.String())
			}
			return
		}
		s.sink.OnMessage(context.Background(), inboundMessage(s.instanceID, e, s.client, s.maxMediaBytes))
	case *events.Receipt:
		if s.sink == nil {
			return
		}
		s.sink.OnReceipt(context.Background(), receiptEvent(s.instanceID, e))
	case *events.HistorySync:
		s.observeHistorySync(e)
	case *events.OfflineSyncPreview:
		s.history.ObservePreview(e.Total)
	case *events.OfflineSyncCompleted:
		s.history.MarkComplete()
	case *events.PairSuccess:
		s.setStatus(session.StatusConnected, e.ID.String(), "")
	default:
		status, reason, ok := connectionUpdate(evt)
		if !ok {
			return
		}
		jid := ""
		if status == session.StatusConnected {
			jid = s.client.Store.GetJID().String()
		}
		s.setStatus(status, jid, reason)
	}
}

// connectionUpdate maps the connection-related library events onto a session
// status and a human-readable reason.
func connectionUpdate(evt any) (session.Status, string, bool) {
	switch e := evt.(type) {
	case *events.Connected:
		return session.StatusConnected, "", true
	case *events.Disconnected:
		return session.StatusDisconnected, "", true
	case *events.LoggedOut:
		return session.StatusDisconnected, "logged out: " + e.Reason.String(), true
	case *events.TemporaryBan:
		return session.StatusError, e.String(), true
	case *events.StreamReplaced:
		return session.StatusError, "stream replaced", true
	case *events.ClientOutdated:
		return session.StatusError, "client outdated", true
	case *events.ConnectFailure:
		return session.StatusError, fmt.Sprintf("connect failure: %s (%s)", e.Reason, e.Message), true
	case *events.StreamError:
		return session.StatusError, "stream error " + e.Code, true
	case *events.CATRefreshError:
		return session.StatusError, "CAT refresh failed: " + e.Error.Error(), true
	}
	return "", "", false
}

// inboundMessage translates a received message, extracting the text and, when
// present, the media metadata plus its lazy download callback. maxMediaBytes
// caps the bytes the callback buffers.
func inboundMessage(instanceID uuid.UUID, evt *events.Message, client *whatsmeow.Client, maxMediaBytes int64) session.InboundMessage {
	msg := session.InboundMessage{
		InstanceID: instanceID,
		MessageID:  evt.Info.ID,
		ChatJID:    evt.Info.Chat.String(),
		SenderJID:  senderJID(evt.Info),
		IsGroup:    evt.Info.IsGroup,
		Type:       evt.Info.Type,
		Text:       messageText(evt.Message),
		Timestamp:  evt.Info.Timestamp,
		Raw:        captureRaw(evt),
	}

	mime, filename, length, downloadable := messageMedia(evt.Message)
	if downloadable != nil {
		msg.MediaAvailable = true
		msg.MediaMime = mime
		msg.MediaFilename = filename
		msg.MediaLength = length
		if client != nil {
			msg.MediaDownload = func(ctx context.Context) ([]byte, error) {
				return downloadLimited(ctx, client, downloadable, maxMediaBytes)
			}
		}
	}
	return msg
}

// receiptEvent translates a delivery/read receipt.
func receiptEvent(instanceID uuid.UUID, evt *events.Receipt) session.Receipt {
	ids := make([]string, len(evt.MessageIDs))
	for i, id := range evt.MessageIDs {
		ids[i] = string(id)
	}
	return session.Receipt{
		InstanceID: instanceID,
		MessageIDs: ids,
		ChatJID:    evt.Chat.String(),
		SenderJID:  evt.Sender.String(),
		Status:     string(evt.Type),
		Timestamp:  evt.Timestamp,
		Raw:        captureRaw(evt),
	}
}

// captureRaw serializes the raw upstream event for webhook delivery. It is
// best-effort: a marshal failure returns nil and never fails the event path.
func captureRaw(evt any) json.RawMessage {
	data, err := json.Marshal(evt)
	if err != nil {
		return nil
	}
	return data
}

// editTarget is an edit resolved to the original message id, the replacement
// content and the edit moment.
type editTarget struct {
	originalID string
	content    *waE2E.Message
	timestamp  time.Time
}

// messageEditTarget resolves the edit carried by a Message event, following
// the levels of the pinned library: a MESSAGE_EDIT protocol message points at
// the original through its key and carries the replacement content, while a
// history-sync edit (IsEdit with the id already rewritten) resolves to the
// event id and content. A REVOKE protocol message is not an edit.
func messageEditTarget(e *events.Message) (editTarget, bool) {
	if protoMsg := e.Message.GetProtocolMessage(); protoMsg != nil {
		switch protoMsg.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return editTarget{}, false
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			originalID := protoMsg.GetKey().GetID()
			if originalID == "" {
				return editTarget{}, false
			}
			timestamp := e.Info.Timestamp
			if ms := protoMsg.GetTimestampMS(); ms > 0 {
				timestamp = time.UnixMilli(ms).UTC()
			}
			return editTarget{
				originalID: originalID,
				content:    protoMsg.GetEditedMessage(),
				timestamp:  timestamp,
			}, true
		}
	}
	if e.IsEdit {
		return editTarget{
			originalID: e.Info.ID,
			content:    e.Message,
			timestamp:  e.Info.Timestamp,
		}, true
	}
	return editTarget{}, false
}

// messageDeleteTarget resolves the original message id revoked by a Message
// event. Only a REVOKE protocol message carrying the original key qualifies;
// anything else (including a keyless revoke) is dropped by returning false.
func messageDeleteTarget(e *events.Message) (string, bool) {
	protoMsg := e.Message.GetProtocolMessage()
	if protoMsg == nil || protoMsg.GetType() != waE2E.ProtocolMessage_REVOKE {
		return "", false
	}
	if originalID := protoMsg.GetKey().GetID(); originalID != "" {
		return originalID, true
	}
	return "", false
}

// editMessage translates a resolved edit away from the library types.
func editMessage(instanceID uuid.UUID, evt *events.Message, target editTarget) session.MessageEdit {
	return session.MessageEdit{
		InstanceID: instanceID,
		MessageID:  target.originalID,
		ChatJID:    evt.Info.Chat.String(),
		SenderJID:  senderJID(evt.Info),
		IsGroup:    evt.Info.IsGroup,
		Text:       messageText(target.content),
		Timestamp:  target.timestamp,
		Raw:        captureRaw(evt),
	}
}

// deleteMessage translates a revocation away from the library types.
func deleteMessage(instanceID uuid.UUID, evt *events.Message, originalID string) session.MessageDelete {
	return session.MessageDelete{
		InstanceID: instanceID,
		MessageID:  originalID,
		ChatJID:    evt.Info.Chat.String(),
		SenderJID:  senderJID(evt.Info),
		IsGroup:    evt.Info.IsGroup,
		Timestamp:  evt.Info.Timestamp,
		Raw:        captureRaw(evt),
	}
}

// senderJID resolves the sender of an inbound event, preferring the
// alternative (LID) address the pinned library exposes on MessageSource and
// falling back to the primary JID when the event carries none.
func senderJID(info types.MessageInfo) string {
	if alt := info.SenderAlt; !alt.IsEmpty() {
		return alt.String()
	}
	return info.Sender.String()
}

// messageText extracts the text of a message, using the caption of media
// messages when there is one.
func messageText(msg *waE2E.Message) string {
	switch {
	case msg.GetConversation() != "":
		return msg.GetConversation()
	case msg.GetExtendedTextMessage().GetText() != "":
		return msg.GetExtendedTextMessage().GetText()
	case msg.GetImageMessage().GetCaption() != "":
		return msg.GetImageMessage().GetCaption()
	case msg.GetVideoMessage().GetCaption() != "":
		return msg.GetVideoMessage().GetCaption()
	case msg.GetDocumentMessage().GetCaption() != "":
		return msg.GetDocumentMessage().GetCaption()
	}
	return ""
}

// messageMedia returns the media metadata and the downloadable attachment of a
// message, when it carries media. The length is the size announced by the
// source, or zero when the proto omits it.
func messageMedia(msg *waE2E.Message) (mime, filename string, length int64, downloadable whatsmeow.DownloadableMessage) {
	switch {
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetMimetype(), "", mediaLength(msg.GetImageMessage().GetFileLength()), msg.GetImageMessage()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetMimetype(), "", mediaLength(msg.GetVideoMessage().GetFileLength()), msg.GetVideoMessage()
	case msg.GetAudioMessage() != nil:
		return msg.GetAudioMessage().GetMimetype(), "", mediaLength(msg.GetAudioMessage().GetFileLength()), msg.GetAudioMessage()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetMimetype(), msg.GetDocumentMessage().GetFileName(), mediaLength(msg.GetDocumentMessage().GetFileLength()), msg.GetDocumentMessage()
	}
	return "", "", 0, nil
}

// mediaLength converts the unsigned announced size into a signed byte count,
// clamping values that do not fit so an oversized length never wraps into a
// small one and slips past the consumer's pre-check.
func mediaLength(fileLength uint64) int64 {
	if fileLength > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(fileLength)
}
