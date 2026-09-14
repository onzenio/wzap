package whatsmeow

import (
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types/events"

	"wzap/internal/session"
)

// HistorySyncSnapshot returns the accumulated history-sync feed of the
// instance (progress, conversation batches, contacts). The Import plan
// consumes it after pairing; each session accumulates only its own feed.
func (s *instanceSession) HistorySyncSnapshot() session.HistorySyncSnapshot {
	return s.history.Snapshot()
}

// ResetHistorySync clears the accumulated history-sync feed after a
// successful import, so the next sync starts from zero.
func (s *instanceSession) ResetHistorySync() {
	s.history.Reset()
}

// observeHistorySync folds a history-sync chunk of the pinned library into
// the per-instance accumulator the Import plan consumes. A chunk without data
// is dropped. History messages never reach the message sink: replaying the
// backlog as live messages would flood the mirror, the import reads the
// accumulator deliberately instead.
func (s *instanceSession) observeHistorySync(evt *events.HistorySync) {
	if evt == nil || evt.Data == nil {
		return
	}
	s.history.Observe(historySyncChunk(evt.Data))
}

// historySyncChunk converts one library payload into the session feed chunk:
// the sync type and progress, the conversation batches and the contacts
// (pushnames plus inline contacts).
func historySyncChunk(data *waHistorySync.HistorySync) session.HistorySyncChunk {
	chunk := session.HistorySyncChunk{
		SyncType: data.GetSyncType().String(),
		Progress: data.GetProgress(),
	}
	for _, conv := range data.GetConversations() {
		chunk.Conversations = append(chunk.Conversations, historySyncConversation(conv))
	}
	for _, push := range data.GetPushnames() {
		if id := push.GetID(); id != "" {
			chunk.Contacts = append(chunk.Contacts, session.HistorySyncContact{
				JID:  id,
				Name: push.GetPushname(),
			})
		}
	}
	for _, inline := range data.GetInlineContacts() {
		jid := inline.GetPnJID()
		if jid == "" {
			jid = inline.GetLidJID()
		}
		if jid == "" {
			continue
		}
		name := inline.GetFullName()
		if name == "" {
			name = inline.GetFirstName()
		}
		chunk.Contacts = append(chunk.Contacts, session.HistorySyncContact{JID: jid, Name: name})
	}
	return chunk
}

// historySyncConversation converts one library conversation batch. Message
// attribution is best-effort: the explicit participant first, then the chat
// itself for direct chats.
func historySyncConversation(conv *waHistorySync.Conversation) session.HistorySyncConversation {
	out := session.HistorySyncConversation{ChatJID: conv.GetID(), Name: conv.GetName()}
	for _, item := range conv.GetMessages() {
		webMsg := item.GetMessage()
		if webMsg == nil || webMsg.GetKey().GetID() == "" {
			continue
		}
		msg := session.HistorySyncMessage{
			MessageID: webMsg.GetKey().GetID(),
			ChatJID:   conv.GetID(),
			SenderJID: historySender(conv.GetID(), webMsg),
			IsFromMe:  webMsg.GetKey().GetFromMe(),
			Text:      messageText(webMsg.GetMessage()),
		}
		if ts := webMsg.GetMessageTimestamp(); ts > 0 {
			msg.Timestamp = time.Unix(int64(ts), 0).UTC()
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}

// historySender resolves the author of a historical message.
func historySender(chatJID string, webMsg *waWeb.WebMessageInfo) string {
	if participant := webMsg.GetParticipant(); participant != "" {
		return participant
	}
	if participant := webMsg.GetKey().GetParticipant(); participant != "" {
		return participant
	}
	return chatJID
}
