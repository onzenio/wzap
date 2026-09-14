package whatsmeow

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"wzap/internal/session"
)

// revokeEvent synthesizes the post-unwrap shape the library dispatches for a
// delete-for-everyone: the message carries a REVOKE protocol message whose key
// points at the original.
func revokeEvent(chat, sender types.JID, originalID string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            "STANZA-REVOKE",
			Timestamp:     time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chat.String()),
					ID:        proto.String(originalID),
				},
			},
		},
	}
}

// protocolEditEvent synthesizes the post-unwrap shape the library dispatches
// for a live edit: a MESSAGE_EDIT protocol message with the original key and
// the replacement content.
func protocolEditEvent(chat, sender types.JID, originalID, newText string, editMS int64) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            "STANZA-EDIT",
			Timestamp:     time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chat.String()),
					ID:        proto.String(originalID),
				},
				EditedMessage: &waE2E.Message{Conversation: proto.String(newText)},
				TimestampMS:   proto.Int64(editMS),
			},
		},
	}
}

// historyEditEvent synthesizes the history-sync shape (ParseWebMessage output):
// the info id is already the original and the message the replacement content.
func historyEditEvent(chat, sender types.JID, originalID, newText string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            originalID,
			Timestamp:     time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{Conversation: proto.String(newText)},
		IsEdit:  true,
	}
}

func dispatchSession(t *testing.T, sink session.EventSink) *instanceSession {
	t.Helper()
	sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	return sess
}

// TestDispatchRevokeCallsOnMessageDelete pins the delete path: a REVOKE
// protocol message reaches the sink with the original id, not the stanza id.
func TestDispatchRevokeCallsOnMessageDelete(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	sess.dispatch(revokeEvent(chat, sender, "ORIGINAL-1"))

	if got := sink.deleteCount(); got != 1 {
		t.Fatalf("deletes recorded = %d, want 1", got)
	}
	del := sink.lastDelete(t)
	if del.MessageID != "ORIGINAL-1" {
		t.Errorf("delete message_id = %q, want the original %q", del.MessageID, "ORIGINAL-1")
	}
	if del.ChatJID != chat.String() {
		t.Errorf("delete chat_jid = %q, want %q", del.ChatJID, chat.String())
	}
	if del.SenderJID != sender.String() {
		t.Errorf("delete sender_jid = %q, want %q", del.SenderJID, sender.String())
	}
	if sink.messageCount() != 0 {
		t.Error("a revoke must not also surface as a plain message")
	}
}

// TestDispatchProtocolEditCallsOnMessageEdit pins the live edit path: the
// original id plus the replacement content and the edit moment.
func TestDispatchProtocolEditCallsOnMessageEdit(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	editMS := time.Date(2026, 9, 14, 12, 5, 0, 0, time.UTC).UnixMilli()
	sess.dispatch(protocolEditEvent(chat, sender, "ORIGINAL-2", "texto corrigido", editMS))

	if got := sink.editCount(); got != 1 {
		t.Fatalf("edits recorded = %d, want 1", got)
	}
	edit := sink.lastEdit(t)
	if edit.MessageID != "ORIGINAL-2" {
		t.Errorf("edit message_id = %q, want the original %q", edit.MessageID, "ORIGINAL-2")
	}
	if edit.Text != "texto corrigido" {
		t.Errorf("edit text = %q, want the replacement content", edit.Text)
	}
	if edit.Timestamp.UnixMilli() != editMS {
		t.Errorf("edit timestamp = %v, want the edit moment %d", edit.Timestamp, editMS)
	}
	if sink.messageCount() != 0 {
		t.Error("an edit must not also surface as a plain message")
	}
}

// TestDispatchHistoryEditCallsOnMessageEdit pins the history-sync edit shape:
// IsEdit with the rewritten id reaches the sink as an edit.
func TestDispatchHistoryEditCallsOnMessageEdit(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	sess.dispatch(historyEditEvent(chat, sender, "ORIGINAL-3", "conteúdo novo"))

	if got := sink.editCount(); got != 1 {
		t.Fatalf("edits recorded = %d, want 1", got)
	}
	if edit := sink.lastEdit(t); edit.MessageID != "ORIGINAL-3" || edit.Text != "conteúdo novo" {
		t.Errorf("edit = %+v, want original ORIGINAL-3 with the new content", edit)
	}
}

// TestDispatchRevokeWithoutKeyIsDropped pins the guard: a REVOKE without the
// original key never reaches the sink.
func TestDispatchRevokeWithoutKeyIsDropped(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	evt := revokeEvent(chat, sender, "")
	evt.Message.GetProtocolMessage().Key = nil
	sess.dispatch(evt)

	if got := sink.deleteCount(); got != 0 {
		t.Errorf("deletes recorded = %d, want none without the original key", got)
	}
	if sink.messageCount() != 0 {
		t.Error("a keyless revoke must not surface as a plain message either")
	}
}

// TestDispatchPlainMessageStillCallsOnMessage guards the existing path: a
// regular message keeps flowing to OnMessage after the edit/delete fork.
func TestDispatchPlainMessageStillCallsOnMessage(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	sess.dispatch(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            "PLAIN-1",
			Timestamp:     time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String("oi")},
	})

	if sink.messageCount() != 1 {
		t.Fatalf("messages recorded = %d, want 1", sink.messageCount())
	}
	if sink.editCount() != 0 || sink.deleteCount() != 0 {
		t.Error("a plain message must not surface as edit/delete")
	}
}

// TestDispatchPrefersAltSenderJID pins the LID rule: when the library exposes
// the alternative (LID) sender address on the event, the translation reports
// it instead of the primary JID, on both the edit and the plain paths.
func TestDispatchPrefersAltSenderJID(t *testing.T) {
	chat := types.NewJID("5511999999999", types.DefaultUserServer)
	sender := types.NewJID("5511888888888", types.DefaultUserServer)
	alt := types.NewJID("100000000000001", types.HiddenUserServer)
	sink := &recordingSink{}
	sess := dispatchSession(t, sink)

	edited := historyEditEvent(chat, sender, "ORIG-LID", "novo texto")
	edited.Info.SenderAlt = alt
	sess.dispatch(edited)

	plain := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: alt},
			ID:            "PLAIN-LID",
			Timestamp:     time.Now().UTC(),
		},
		Message: &waE2E.Message{Conversation: proto.String("oi")},
	}
	sess.dispatch(plain)

	if edit := sink.lastEdit(t); edit.SenderJID != alt.String() {
		t.Errorf("edit sender_jid = %q, want the alt-JID %q", edit.SenderJID, alt.String())
	}
	sink.mu.Lock()
	var plainSender string
	for _, msg := range sink.messages {
		if msg.MessageID == "PLAIN-LID" {
			plainSender = msg.SenderJID
		}
	}
	sink.mu.Unlock()
	if plainSender != alt.String() {
		t.Errorf("message sender_jid = %q, want the alt-JID %q", plainSender, alt.String())
	}
}
