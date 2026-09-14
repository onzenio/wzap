package whatsmeow

import (
	"testing"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func historyWebMessage(id, chat string, fromMe bool, text string, timestamp uint64, participant string) *waWeb.WebMessageInfo {
	return &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			ID:          proto.String(id),
			RemoteJID:   proto.String(chat),
			FromMe:      proto.Bool(fromMe),
			Participant: proto.String(participant),
		},
		Message:          &waE2E.Message{Conversation: proto.String(text)},
		MessageTimestamp: proto.Uint64(timestamp),
		Participant:      proto.String(participant),
	}
}

// synthesizedHistorySync builds the lib-level payload of one history-sync
// chunk: two conversations (DM + group), pushnames and inline contacts.
func synthesizedHistorySync() *events.HistorySync {
	dm := "5511999999999@s.whatsapp.net"
	group := "120363000000000000@g.us"
	return &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType:   waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
			Progress:   proto.Uint32(40),
			ChunkOrder: proto.Uint32(1),
			Conversations: []*waHistorySync.Conversation{
				{
					ID:   proto.String(dm),
					Name: proto.String("Fulano"),
					Messages: []*waHistorySync.HistorySyncMsg{
						{Message: historyWebMessage("HMSG-1", dm, false, "oi", 1726147200, "")},
						{Message: historyWebMessage("HMSG-2", dm, true, "olá", 1726147260, "")},
					},
				},
				{
					ID:   proto.String(group),
					Name: proto.String("Grupo da loja"),
					Messages: []*waHistorySync.HistorySyncMsg{
						{Message: historyWebMessage("HMSG-3", group, false, "bom dia", 1726147300, "5511888888888@s.whatsapp.net")},
					},
				},
			},
			Pushnames: []*waHistorySync.Pushname{
				{ID: proto.String("5511888888888@s.whatsapp.net"), Pushname: proto.String("Beltrano")},
			},
			InlineContacts: []*waHistorySync.InlineContact{
				{PnJID: proto.String("5511777777777@s.whatsapp.net"), FullName: proto.String("Ciclana")},
			},
		},
	}
}

// TestDispatchHistorySyncAccumulatesBatchesAndContacts pins the feed the
// Import plan consumes: one lib chunk becomes progress plus conversation
// batches plus contacts in the per-instance accumulator.
func TestDispatchHistorySyncAccumulatesBatchesAndContacts(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	sess.dispatch(synthesizedHistorySync())
	snap := sess.HistorySyncSnapshot()

	if snap.Chunks != 1 {
		t.Fatalf("snapshot chunks = %d, want 1", snap.Chunks)
	}
	if snap.Progress != 40 {
		t.Errorf("snapshot progress = %d, want 40", snap.Progress)
	}
	if snap.SyncType != "INITIAL_BOOTSTRAP" {
		t.Errorf("snapshot sync_type = %q, want INITIAL_BOOTSTRAP", snap.SyncType)
	}
	if len(snap.Conversations) != 2 {
		t.Fatalf("snapshot conversations = %+v, want DM + group", snap.Conversations)
	}
	byChat := map[string]int{}
	for _, conv := range snap.Conversations {
		byChat[conv.ChatJID] = len(conv.Messages)
	}
	if byChat["5511999999999@s.whatsapp.net"] != 2 {
		t.Errorf("DM messages = %+v, want HMSG-1/HMSG-2", snap.Conversations)
	}
	if byChat["120363000000000000@g.us"] != 1 {
		t.Errorf("group messages = %+v, want HMSG-3", snap.Conversations)
	}
	for _, conv := range snap.Conversations {
		for _, msg := range conv.Messages {
			if msg.MessageID == "" || msg.Timestamp.IsZero() {
				t.Errorf("history message = %+v, want id and timestamp", msg)
			}
		}
	}
	names := map[string]string{}
	for _, c := range snap.Contacts {
		names[c.JID] = c.Name
	}
	if names["5511888888888@s.whatsapp.net"] != "Beltrano" {
		t.Errorf("contacts = %+v, want the Beltrano pushname", snap.Contacts)
	}
	if names["5511777777777@s.whatsapp.net"] != "Ciclana" {
		t.Errorf("contacts = %+v, want the Ciclana inline contact", snap.Contacts)
	}
}

// TestDispatchHistorySyncMergesChunks pins that repeated chunks accumulate
// without duplicating messages.
func TestDispatchHistorySyncMergesChunks(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	sess.dispatch(synthesizedHistorySync())
	sess.dispatch(synthesizedHistorySync())
	snap := sess.HistorySyncSnapshot()

	if snap.Chunks != 2 {
		t.Errorf("snapshot chunks = %d, want 2", snap.Chunks)
	}
	total := 0
	for _, conv := range snap.Conversations {
		total += len(conv.Messages)
	}
	if total != 3 {
		t.Errorf("accumulated messages = %d, want 3 without duplicates", total)
	}
}

// TestDispatchHistorySyncGroupMessageKeepsParticipant pins that group history
// attributes the message to its author, not the group.
func TestDispatchHistorySyncGroupMessageKeepsParticipant(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	sess.dispatch(synthesizedHistorySync())
	snap := sess.HistorySyncSnapshot()

	for _, conv := range snap.Conversations {
		if conv.ChatJID != "120363000000000000@g.us" {
			continue
		}
		if len(conv.Messages) != 1 {
			t.Fatalf("group messages = %+v, want HMSG-3", conv.Messages)
		}
		if got := conv.Messages[0].SenderJID; got != "5511888888888@s.whatsapp.net" {
			t.Errorf("group sender = %q, want the participant", got)
		}
		return
	}
	t.Fatal("group conversation missing from the snapshot")
}

// TestDispatchOfflineSyncPreviewAndCompleted pins the progress lifecycle:
// preview totals then the completion flag.
func TestDispatchOfflineSyncPreviewAndCompleted(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	sess.dispatch(&events.OfflineSyncPreview{Total: 7, Messages: 5})
	if got := sess.HistorySyncSnapshot().Total; got != 7 {
		t.Fatalf("snapshot total = %d, want the preview 7", got)
	}
	if sess.HistorySyncSnapshot().Completed {
		t.Fatal("snapshot completed before the sync finished")
	}

	sess.dispatch(&events.OfflineSyncCompleted{Count: 7})
	if !sess.HistorySyncSnapshot().Completed {
		t.Error("snapshot completed = false, want true after OfflineSyncCompleted")
	}
}

// TestHistorySyncSnapshotsArePerInstance pins accumulator isolation across
// sessions: each instance feeds only its own snapshot.
func TestHistorySyncSnapshotsArePerInstance(t *testing.T) {
	first, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	second, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	first.dispatch(synthesizedHistorySync())

	if got := len(first.HistorySyncSnapshot().Conversations); got != 2 {
		t.Errorf("first conversations = %d, want 2", got)
	}
	if got := len(second.HistorySyncSnapshot().Conversations); got != 0 {
		t.Errorf("second conversations = %d, want none", got)
	}
}

// TestDispatchHistorySyncNilDataIsDropped pins the guard: a chunk without data
// never poisons the accumulator.
func TestDispatchHistorySyncNilDataIsDropped(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	sess.dispatch(&events.HistorySync{})
	if got := sess.HistorySyncSnapshot().Chunks; got != 0 {
		t.Errorf("snapshot chunks = %d, want none for dataless sync", got)
	}
}
