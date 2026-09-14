package sessiontest

import (
	"testing"

	"github.com/google/uuid"

	"wzap/internal/session"
)

// TestFakeSessionHistorySyncSnapshotAccumulates pins the fake side of the feed
// the Import plan consumes: observed chunks surface in the snapshot.
func TestFakeSessionHistorySyncSnapshotAccumulates(t *testing.T) {
	sess := NewSession(uuid.New(), nil)

	sess.ObserveHistorySync(session.HistorySyncChunk{
		SyncType: "RECENT",
		Progress: 50,
		Conversations: []session.HistorySyncConversation{{
			ChatJID:  "5511999999999@s.whatsapp.net",
			Messages: []session.HistorySyncMessage{{MessageID: "H-1"}},
		}},
		Contacts: []session.HistorySyncContact{{JID: "5511888888888@s.whatsapp.net", Name: "Beltrano"}},
	})

	snap := sess.HistorySyncSnapshot()
	if snap.Chunks != 1 || snap.Progress != 50 || snap.SyncType != "RECENT" {
		t.Errorf("snapshot = %+v, want the observed chunk", snap)
	}
	if len(snap.Conversations) != 1 || len(snap.Contacts) != 1 {
		t.Errorf("snapshot = %+v, want batches and contacts", snap)
	}
}
