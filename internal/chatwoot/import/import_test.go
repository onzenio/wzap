package chatimport

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"wzap/internal/session"
	"wzap/internal/storage/postgres/postgrestest"
)

// minimalChatwootSchema mirrors the Chatwoot tables the import touches, with
// only the columns it reads or writes.
const minimalChatwootSchema = `
CREATE TABLE labels (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	account_id BIGINT NOT NULL,
	title VARCHAR(255) NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (account_id, title)
);
CREATE TABLE contacts (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	account_id BIGINT NOT NULL,
	identifier VARCHAR(255),
	name VARCHAR(255) NOT NULL DEFAULT '',
	email VARCHAR(255),
	phone_number VARCHAR(255),
	additional_attributes JSONB NOT NULL DEFAULT '{}',
	custom_attributes JSONB NOT NULL DEFAULT '{}',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (identifier, account_id)
);
CREATE TABLE tags (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	name VARCHAR(255) NOT NULL UNIQUE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE taggings (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	tag_id BIGINT NOT NULL REFERENCES tags(id),
	taggable_type VARCHAR(255) NOT NULL,
	taggable_id BIGINT NOT NULL,
	context VARCHAR(255) NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE (tag_id, taggable_type, taggable_id, context)
);
`

// TestImportContactsUpsertsIndividualsAndGroups imports 2 contacts plus 1
// group: upsert keyed by (identifier, account_id), groups stored with NULL
// phone and " (GROUP)" name suffix, every contact tagged once, and a second
// run duplicates nothing.
func TestImportContactsUpsertsIndividualsAndGroups(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, minimalChatwootSchema); err != nil {
		t.Fatalf("create minimal Chatwoot schema: %v", err)
	}

	// Pre-seed Alice with a stale name to prove the upsert updates.
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (account_id, identifier, name, phone_number) VALUES (1, '5511999887766', 'Old', '+5511999887766')`); err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	contacts := []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
		{JID: "5511999887755@s.whatsapp.net", Name: "Bob"},
		{JID: "120363012345678@g.us", Name: "Family"},
	}

	got, err := ImportContacts(ctx, pool, "1", "Test Inbox", contacts)
	if err != nil {
		t.Fatalf("ImportContacts() error = %v", err)
	}
	if got != 3 {
		t.Fatalf("ImportContacts() = %d, want 3", got)
	}

	var name, phone *string
	if err := pool.QueryRow(ctx,
		`SELECT name, phone_number FROM contacts WHERE account_id = 1 AND identifier = '5511999887766'`).Scan(&name, &phone); err != nil {
		t.Fatalf("query Alice: %v", err)
	}
	if name == nil || *name != "Alice" {
		t.Errorf("Alice name = %v, want Alice (upsert update)", strVal(name))
	}
	if phone == nil || *phone != "+5511999887766" {
		t.Errorf("Alice phone = %v, want +5511999887766", strVal(phone))
	}

	if err := pool.QueryRow(ctx,
		`SELECT name, phone_number FROM contacts WHERE account_id = 1 AND identifier = '120363012345678'`).Scan(&name, &phone); err != nil {
		t.Fatalf("query group: %v", err)
	}
	if name == nil || *name != "Family (GROUP)" {
		t.Errorf("group name = %v, want Family (GROUP)", strVal(name))
	}
	if phone != nil {
		t.Errorf("group phone = %q, want NULL", *phone)
	}

	counts := queryCounts(t, ctx, pool)
	if counts["contacts"] != 3 {
		t.Errorf("contacts = %d, want 3", counts["contacts"])
	}
	if counts["tags"] != 1 {
		t.Errorf("tags = %d, want 1 (single insert, no Evolution double-insert)", counts["tags"])
	}
	if counts["taggings"] != 3 {
		t.Errorf("taggings = %d, want 3 (one per contact)", counts["taggings"])
	}
	if counts["labels"] != 1 {
		t.Errorf("labels = %d, want 1", counts["labels"])
	}

	// Re-run: idempotent, duplicates nothing.
	got, err = ImportContacts(ctx, pool, "1", "Test Inbox", contacts)
	if err != nil {
		t.Fatalf("ImportContacts() second run error = %v", err)
	}
	if got != 3 {
		t.Fatalf("ImportContacts() second run = %d, want 3", got)
	}
	rerun := queryCounts(t, ctx, pool)
	for table, want := range counts {
		if rerun[table] != want {
			t.Errorf("%s after re-run = %d, want %d (must not duplicate)", table, rerun[table], want)
		}
	}
}

// TestImportContactsRejectsInvalidAccountID names the operation on bad input.
func TestImportContactsRejectsInvalidAccountID(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, minimalChatwootSchema); err != nil {
		t.Fatalf("create minimal Chatwoot schema: %v", err)
	}

	contacts := []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	}
	if _, err := ImportContacts(ctx, pool, "abc", "Test Inbox", contacts); err == nil {
		t.Fatal("ImportContacts() error = nil, want error for non-numeric account id")
	}
}

func strVal(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

func queryCounts(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
},
) map[string]int {
	t.Helper()

	counts := make(map[string]int, 4)
	for _, table := range []string{"contacts", "tags", "taggings", "labels"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}
