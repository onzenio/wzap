package whatsmeow

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
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
		s.sink.OnMessage(context.Background(), inboundMessage(s.instanceID, e, s.client, s.maxMediaBytes))
	case *events.Receipt:
		if s.sink == nil {
			return
		}
		s.sink.OnReceipt(context.Background(), receiptEvent(s.instanceID, e))
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
		SenderJID:  evt.Info.Sender.String(),
		IsGroup:    evt.Info.IsGroup,
		Type:       evt.Info.Type,
		Text:       messageText(evt.Message),
		Timestamp:  evt.Info.Timestamp,
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
	}
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
