package chatimport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage/postgres/postgrestest"
)

// fakeFeed is an in-memory HistoryFeed: the snapshot the run consumes plus a
// reset counter proving the accumulators are cleared.
type fakeFeed struct {
	snap   session.HistorySyncSnapshot
	resets int
}

func (f *fakeFeed) HistorySyncSnapshot() session.HistorySyncSnapshot { return f.snap }
func (f *fakeFeed) ResetHistorySync() {
	f.resets++
	f.snap = session.HistorySyncSnapshot{}
}

// fakeInboxes replays one inbox list for the inbox lookup.
type fakeInboxes struct {
	inboxes []client.Inbox
	err     error
}

func (f *fakeInboxes) ListInboxes(context.Context) ([]client.Inbox, error) {
	return f.inboxes, f.err
}

// TestRunImportNoticesCountsAndClears pins the manual/auto run: contacts and
// messages import from the feed snapshot, the operational poster gets a
// start and a pt-BR result notice, the accumulator is cleared, and the
// returned count is the messages imported.
func TestRunImportNoticesCountsAndClears(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, minimalChatwootSchema+extendedChatwootSchema); err != nil {
		t.Fatalf("create extended Chatwoot schema: %v", err)
	}
	var agentID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (name) VALUES ('Agent') RETURNING id`).Scan(&agentID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO access_tokens (owner_type, owner_id, token) VALUES ('User', $1, 'secret-token')`, agentID); err != nil {
		t.Fatalf("seed access token: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	feed := &fakeFeed{snap: session.HistorySyncSnapshot{
		Contacts: []session.HistorySyncContact{
			{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
		},
		Conversations: []session.HistorySyncConversation{{
			ChatJID: "5511999887766@s.whatsapp.net",
			Messages: []session.HistorySyncMessage{
				{MessageID: "H1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: now, Text: "hello"},
				{MessageID: "H2", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "me@s.whatsapp.net", IsFromMe: true, Timestamp: now.Add(time.Minute), Text: "hi back"},
			},
		}},
	}}
	var notices []string
	deps := RunDeps{
		Pool:   pool,
		Config: model.ChatwootConfig{InstanceID: uuid.New(), Enabled: true, AccountID: "1", Token: "secret-token", NameInbox: "Test Inbox", ImportContacts: true, ImportMessages: true},
		Inboxes: &fakeInboxes{inboxes: []client.Inbox{
			{ID: 7, Name: "Test Inbox"},
		}},
		Feed: feed,
		Poster: func(_ context.Context, text string) error {
			notices = append(notices, text)
			return nil
		},
	}

	got, err := RunImport(ctx, deps)
	if err != nil {
		t.Fatalf("RunImport() error = %v", err)
	}
	if got != 2 {
		t.Fatalf("RunImport() = %d, want 2 messages", got)
	}
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want start + result", len(notices))
	}
	for _, notice := range notices {
		if !strings.Contains(strings.ToLower(notice), "importa") {
			t.Errorf("notice = %q, want pt-BR import text", notice)
		}
	}
	if feed.resets != 1 {
		t.Errorf("accumulator resets = %d, want 1 (cleared after import)", feed.resets)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&total); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if total != 2 {
		t.Errorf("messages = %d, want 2", total)
	}
}

// TestRunImportInertWithoutPool pins the guard: without a pool the run is a
// no-op with no notices and no reset.
func TestRunImportInertWithoutPool(t *testing.T) {
	feed := &fakeFeed{snap: session.HistorySyncSnapshot{
		Contacts: []session.HistorySyncContact{{JID: "5511999887766@s.whatsapp.net", Name: "Alice"}},
	}}
	posted := 0
	got, err := RunImport(context.Background(), RunDeps{
		Config:  model.ChatwootConfig{AccountID: "1", NameInbox: "Test Inbox", ImportContacts: true, ImportMessages: true},
		Inboxes: &fakeInboxes{inboxes: []client.Inbox{{ID: 7, Name: "Test Inbox"}}},
		Feed:    feed,
		Poster: func(context.Context, string) error {
			posted++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunImport() error = %v, want nil when inert", err)
	}
	if got != 0 {
		t.Fatalf("RunImport() = %d, want 0 when inert", got)
	}
	if posted != 0 {
		t.Errorf("notices = %d, want 0 when inert", posted)
	}
	if feed.resets != 0 {
		t.Errorf("resets = %d, want 0 when inert", feed.resets)
	}
}

// TestRunImportMissingInboxNamesTheOperation pins that an unprovisioned
// inbox fails the run instead of importing nowhere.
func TestRunImportMissingInboxNamesTheOperation(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, minimalChatwootSchema+extendedChatwootSchema); err != nil {
		t.Fatalf("create extended Chatwoot schema: %v", err)
	}
	feed := &fakeFeed{}
	_, err := RunImport(ctx, RunDeps{
		Pool:    pool,
		Config:  model.ChatwootConfig{AccountID: "1", NameInbox: "Missing", ImportMessages: true},
		Inboxes: &fakeInboxes{inboxes: []client.Inbox{{ID: 7, Name: "Other"}}},
		Feed:    feed,
	})
	if err == nil {
		t.Fatal("RunImport() error = nil, want error for missing inbox")
	}
	if !strings.Contains(err.Error(), "run import") {
		t.Fatalf("RunImport() error = %q, want it to name the operation", err)
	}
}
