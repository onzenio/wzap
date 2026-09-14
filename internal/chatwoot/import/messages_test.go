package chatimport

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/session"
	"wzap/internal/storage/postgres/postgrestest"
)

// extendedChatwootSchema adds the conversation/message tables the message
// import touches to the contact tables of minimalChatwootSchema. Column names
// mirror the real Chatwoot schema (messages.message_type 0 incoming / 1
// outgoing, conversations.status/display_id, contact_inboxes.source_id,
// access_tokens owner/token); constraints are limited to the ones the import
// relies on.
const extendedChatwootSchema = `
CREATE TABLE users (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	name VARCHAR(255) NOT NULL DEFAULT ''
);
CREATE TABLE access_tokens (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	owner_type VARCHAR(255),
	owner_id BIGINT,
	token VARCHAR(255) NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE contact_inboxes (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	contact_id BIGINT NOT NULL,
	inbox_id BIGINT NOT NULL,
	source_id TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (inbox_id, source_id)
);
CREATE TABLE conversations (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	account_id BIGINT NOT NULL,
	inbox_id BIGINT NOT NULL,
	status INT NOT NULL DEFAULT 0,
	contact_id BIGINT,
	contact_inbox_id BIGINT,
	display_id INT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (account_id, display_id)
);
CREATE TABLE messages (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	content TEXT,
	account_id BIGINT NOT NULL,
	inbox_id BIGINT NOT NULL,
	conversation_id BIGINT NOT NULL,
	message_type INT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	private BOOLEAN NOT NULL DEFAULT FALSE,
	status INT NOT NULL DEFAULT 0,
	source_id TEXT,
	content_type INT NOT NULL DEFAULT 0,
	content_attributes JSONB NOT NULL DEFAULT '{}',
	sender_type VARCHAR(255),
	sender_id BIGINT
);
`

// seedMessageWorld creates the contact/message tables plus one agent user
// holding token "secret-token", and upserts the given contacts. It returns
// the agent user id.
func seedMessageWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contacts []session.HistorySyncContact) int64 {
	t.Helper()
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
	if len(contacts) > 0 {
		if _, err := ImportContacts(ctx, pool, "1", "Test Inbox", contacts); err != nil {
			t.Fatalf("seed contacts: %v", err)
		}
	}
	return agentID
}

// messageContentsByConversation returns the contents of every imported
// message of one contact identifier, oldest first.
func messageContentsByConversation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, identifier string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT m.content FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		JOIN contacts ct ON ct.id = c.contact_id
		WHERE ct.identifier = $1 AND ct.account_id = 1
		ORDER BY m.created_at ASC, m.id ASC`, identifier)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var content *string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		if content == nil {
			out = append(out, "<NULL>")
			continue
		}
		out = append(out, *content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read messages: %v", err)
	}
	return out
}

// TestImportMessagesOrdersDedupsAndAttributes imports out-of-order history
// across two phones plus one outgoing message: rows land ordered by
// phone+time, dedup by pre-existing source_id WAID:, fromMe maps to
// message_type 1 with the access_tokens sender, and a re-run imports
// nothing.
func TestImportMessagesOrdersDedupsAndAttributes(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	agentID := seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
		{JID: "5511999887755@s.whatsapp.net", Name: "Bob"},
	})

	base := time.Now().UTC().Truncate(time.Second)
	msgs := []session.HistorySyncMessage{
		{MessageID: "M3", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base.Add(3 * time.Minute), Text: "third"},
		{MessageID: "M1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base.Add(1 * time.Minute), Text: "first"},
		{MessageID: "M2", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base.Add(2 * time.Minute), Text: "second"},
		{MessageID: "B1", ChatJID: "5511999887755@s.whatsapp.net", SenderJID: "5511999887755@s.whatsapp.net", Timestamp: base.Add(1 * time.Minute), Text: "bob says"},
		{MessageID: "O1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "me@s.whatsapp.net", IsFromMe: true, Timestamp: base.Add(4 * time.Minute), Text: "reply"},
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1", Token: "secret-token"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 5 {
		t.Fatalf("ImportMessages() = %d, want 5", got)
	}

	if contents := messageContentsByConversation(t, ctx, pool, "5511999887766"); strings.Join(contents, "|") != "first|second|third|reply" {
		t.Errorf("alice contents = %q, want phone+time order first|second|third|reply", strings.Join(contents, "|"))
	}
	if contents := messageContentsByConversation(t, ctx, pool, "5511999887755"); strings.Join(contents, "|") != "bob says" {
		t.Errorf("bob contents = %q, want bob says", strings.Join(contents, "|"))
	}

	var msgType int
	var senderType *string
	var senderID *int64
	if err := pool.QueryRow(ctx, `SELECT message_type, sender_type, sender_id FROM messages WHERE source_id = 'WAID:O1'`).Scan(&msgType, &senderType, &senderID); err != nil {
		t.Fatalf("query outgoing: %v", err)
	}
	if msgType != 1 {
		t.Errorf("outgoing message_type = %d, want 1", msgType)
	}
	if senderType == nil || *senderType != "User" {
		t.Errorf("outgoing sender_type = %v, want User (from access_tokens)", strVal(senderType))
	}
	if senderID == nil || *senderID != agentID {
		t.Errorf("outgoing sender_id = %v, want agent %d", senderID, agentID)
	}
	if err := pool.QueryRow(ctx, `SELECT message_type, sender_type FROM messages WHERE source_id = 'WAID:M1'`).Scan(&msgType, &senderType); err != nil {
		t.Fatalf("query incoming: %v", err)
	}
	if msgType != 0 {
		t.Errorf("incoming message_type = %d, want 0", msgType)
	}
	if senderType == nil || *senderType != "Contact" {
		t.Errorf("incoming sender_type = %v, want Contact", strVal(senderType))
	}

	again, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1", Token: "secret-token"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() re-run error = %v", err)
	}
	if again != 0 {
		t.Errorf("ImportMessages() re-run = %d, want 0 (dedup by source_id)", again)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&total); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if total != 5 {
		t.Errorf("messages after re-run = %d, want 5", total)
	}
}

// TestImportMessagesSkipsContentlessWithoutPlaceholder pins the window rule:
// messages without content are skipped, unless placeholders are configured,
// in which case they land with the placeholder text.
func TestImportMessagesSkipsContentlessWithoutPlaceholder(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	})

	base := time.Now().UTC().Truncate(time.Second)
	msgs := []session.HistorySyncMessage{
		{MessageID: "E1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base, Text: "   "},
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("ImportMessages() content-less without placeholder = %d, want 0 skipped", got)
	}

	got, err = ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1", Placeholder: true}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() with placeholder error = %v", err)
	}
	if got != 1 {
		t.Fatalf("ImportMessages() with placeholder = %d, want 1", got)
	}
	contents := messageContentsByConversation(t, ctx, pool, "5511999887766")
	if len(contents) != 1 || contents[0] == "" {
		t.Fatalf("placeholder contents = %q, want the placeholder text", strings.Join(contents, "|"))
	}
}

// TestImportMessagesDaysLimitCutsOldHistory pins days_limit: with a 7-day
// window only the recent message imports.
func TestImportMessagesDaysLimitCutsOldHistory(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	})

	now := time.Now().UTC().Truncate(time.Second)
	msgs := []session.HistorySyncMessage{
		{MessageID: "OLD", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: now.Add(-30 * 24 * time.Hour), Text: "old"},
		{MessageID: "NEW", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: now.Add(-24 * time.Hour), Text: "new"},
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1", DaysLimit: 7}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 1 {
		t.Fatalf("ImportMessages() with days_limit 7 = %d, want 1 (old cut)", got)
	}
	if contents := messageContentsByConversation(t, ctx, pool, "5511999887766"); strings.Join(contents, "|") != "new" {
		t.Errorf("contents = %q, want only new", strings.Join(contents, "|"))
	}
}

// TestImportMessagesInBatches imports across the chunk boundary: 1200
// messages in one call all land, proving the batching.
func TestImportMessagesInBatches(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	})

	base := time.Now().UTC().Truncate(time.Second)
	var msgs []session.HistorySyncMessage
	for i := 0; i < 1200; i++ {
		msgs = append(msgs, session.HistorySyncMessage{
			MessageID: "BATCH-" + strconv.Itoa(i),
			ChatJID:   "5511999887766@s.whatsapp.net",
			SenderJID: "5511999887766@s.whatsapp.net",
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Text:      "bulk message",
		})
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 1200 {
		t.Fatalf("ImportMessages() batches = %d, want 1200", got)
	}
}

// TestImportMessagesPropagatesMirrorSourceIDs pins the mirror interplay: a
// source_id the live mirror already wrote dedups the history re-import.
func TestImportMessagesPropagatesMirrorSourceIDs(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO contact_inboxes (contact_id, inbox_id, source_id)
		SELECT id, 7, 'mirror-source' FROM contacts WHERE identifier = '5511999887766' AND account_id = 1`); err != nil {
		t.Fatalf("seed contact_inbox: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conversations (account_id, inbox_id, status, contact_id, contact_inbox_id, display_id)
		SELECT 1, 7, 0, ct.id, ci.id, 1 FROM contacts ct
		JOIN contact_inboxes ci ON ci.contact_id = ct.id AND ci.inbox_id = 7
		WHERE ct.identifier = '5511999887766' AND ct.account_id = 1`); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO messages (content, account_id, inbox_id, conversation_id, message_type, created_at, updated_at, source_id, sender_type)
		SELECT 'live', 1, 7, c.id, 0, NOW(), NOW(), 'WAID:LIVE-1', 'Contact' FROM conversations c
		JOIN contacts ct ON ct.id = c.contact_id WHERE ct.identifier = '5511999887766'`); err != nil {
		t.Fatalf("seed mirror message: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	msgs := []session.HistorySyncMessage{
		{MessageID: "LIVE-1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base, Text: "live"},
		{MessageID: "HIST-1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base.Add(time.Minute), Text: "hist"},
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 1 {
		t.Fatalf("ImportMessages() with mirror source_id = %d, want 1 (mirror row dedups)", got)
	}
}

// TestImportMessagesEnsuresContactsOnDemand pins the messages-only run:
// with no contacts seeded, the message authors are ensured on demand
// (untagged) instead of every row being silently dropped.
func TestImportMessagesEnsuresContactsOnDemand(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, nil)

	base := time.Now().UTC().Truncate(time.Second)
	msgs := []session.HistorySyncMessage{
		{MessageID: "A1", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base, Text: "hello"},
		{MessageID: "A2", ChatJID: "5511999887766@s.whatsapp.net", SenderJID: "me@s.whatsapp.net", IsFromMe: true, Timestamp: base.Add(time.Minute), Text: "hi back"},
		{MessageID: "G1", ChatJID: "120363012345678@g.us", SenderJID: "5511999887766@s.whatsapp.net", Timestamp: base, Text: "group hi"},
	}

	got, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "1"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v", err)
	}
	if got != 3 {
		t.Fatalf("ImportMessages() without seeded contacts = %d, want 3 (authors ensured on demand)", got)
	}

	var phone *string
	if err := pool.QueryRow(ctx, `SELECT phone_number FROM contacts WHERE account_id = 1 AND identifier = '5511999887766'`).Scan(&phone); err != nil {
		t.Fatalf("query ensured contact: %v", err)
	}
	if phone == nil || *phone != "+5511999887766" {
		t.Errorf("ensured contact phone = %v, want +5511999887766", strVal(phone))
	}
	var name *string
	if err := pool.QueryRow(ctx, `SELECT name FROM contacts WHERE account_id = 1 AND identifier = '120363012345678'`).Scan(&name); err != nil {
		t.Fatalf("query ensured group: %v", err)
	}
	if name == nil || *name != "120363012345678 (GROUP)" {
		t.Errorf("ensured group name = %v, want the group suffix", strVal(name))
	}
	var tags int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM taggings`).Scan(&tags); err != nil {
		t.Fatalf("count taggings: %v", err)
	}
	if tags != 0 {
		t.Errorf("taggings = %d, want 0 (on-demand ensure is untagged; tags belong to the bulk flag)", tags)
	}
}

// TestImportMessagesConcurrentTriggersShareDisplayIDs pins the display_id
// race: parallel manual+auto+cron-style imports over the same phones share
// one conversation per contact with distinct display_ids — no
// UNIQUE(account_id, display_id) violation, no duplicated conversation, and
// every row present. Contacts are pre-seeded to isolate the display_id
// allocation from the on-demand contact ensure.
func TestImportMessagesConcurrentTriggersShareDisplayIDs(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	seedMessageWorld(t, ctx, pool, []session.HistorySyncContact{
		{JID: "5511999887701@s.whatsapp.net", Name: "C1"},
		{JID: "5511999887702@s.whatsapp.net", Name: "C2"},
		{JID: "5511999887703@s.whatsapp.net", Name: "C3"},
		{JID: "5511999887704@s.whatsapp.net", Name: "C4"},
	})

	phones := []string{
		"5511999887701@s.whatsapp.net",
		"5511999887702@s.whatsapp.net",
		"5511999887703@s.whatsapp.net",
		"5511999887704@s.whatsapp.net",
	}
	base := time.Now().UTC().Truncate(time.Second)

	const triggers = 8
	const perTrigger = 25
	errs := make(chan error, triggers)
	var wg sync.WaitGroup
	for g := 0; g < triggers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			msgs := make([]session.HistorySyncMessage, 0, perTrigger)
			for i := 0; i < perTrigger; i++ {
				chat := phones[(g+i)%len(phones)]
				msgs = append(msgs, session.HistorySyncMessage{
					MessageID: "G" + strconv.Itoa(g) + "-M" + strconv.Itoa(i),
					ChatJID:   chat,
					SenderJID: chat,
					Timestamp: base.Add(time.Duration(i) * time.Second),
					Text:      "concurrent",
				})
			}
			if _, err := ImportMessages(context.Background(), MessagesDeps{Pool: pool, AccountID: "1"}, 7, msgs); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ImportMessages() concurrent error = %v (want no unique violation)", err)
	}

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&total); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if total != triggers*perTrigger {
		t.Errorf("messages = %d, want %d (all rows present)", total, triggers*perTrigger)
	}
	var conversations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE account_id = 1`).Scan(&conversations); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if conversations != len(phones) {
		t.Errorf("conversations = %d, want %d (one per contact)", conversations, len(phones))
	}
	var distinct int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT display_id) FROM conversations WHERE account_id = 1`).Scan(&distinct); err != nil {
		t.Fatalf("count display ids: %v", err)
	}
	if distinct != len(phones) {
		t.Errorf("distinct display_ids = %d, want %d (no shared allocation)", distinct, len(phones))
	}
}

// TestImportMessagesInertWithoutPool pins the guard: a nil pool imports
// nothing and touches no network.
func TestImportMessagesInertWithoutPool(t *testing.T) {
	msgs := []session.HistorySyncMessage{
		{MessageID: "M1", ChatJID: "5511999887766@s.whatsapp.net", Text: "hi"},
	}
	got, err := ImportMessages(context.Background(), MessagesDeps{AccountID: "1"}, 7, msgs)
	if err != nil {
		t.Fatalf("ImportMessages() error = %v, want nil when inert", err)
	}
	if got != 0 {
		t.Fatalf("ImportMessages() = %d, want 0 when inert", got)
	}
}

// TestImportMessagesRejectsInvalidAccountID names the operation on bad input.
func TestImportMessagesRejectsInvalidAccountID(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, minimalChatwootSchema+extendedChatwootSchema); err != nil {
		t.Fatalf("create extended Chatwoot schema: %v", err)
	}
	msgs := []session.HistorySyncMessage{
		{MessageID: "M1", ChatJID: "5511999887766@s.whatsapp.net", Text: "hi"},
	}
	_, err := ImportMessages(ctx, MessagesDeps{Pool: pool, AccountID: "abc"}, 7, msgs)
	if err == nil {
		t.Fatal("ImportMessages() error = nil, want error for non-numeric account id")
	}
	if !strings.Contains(err.Error(), "import messages") {
		t.Fatalf("ImportMessages() error = %q, want it to name the operation", err)
	}
}
