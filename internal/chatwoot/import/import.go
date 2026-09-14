package chatimport

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/session"
)

const (
	opImportContacts = "chatimport: import contacts"
	// contactChunkSize bounds each multi-row upsert; mirrors the Evolution
	// import batching.
	contactChunkSize = 3000
	// taggableContact and tagContext are the acts-as-taggable-on coordinates
	// Chatwoot uses for contact tags.
	taggableContact = "Contact"
	tagContext      = "tags"
	// groupNameSuffix marks imported group chats, which carry no phone.
	groupNameSuffix = " (GROUP)"
)

// ImportContacts upserts history-sync contacts into the Chatwoot database,
// returning how many contacts were written. Contacts upsert by
// (identifier, account_id); every contact is tagged once with inboxName and
// the inbox label is ensured. Group chats (@g.us) store NULL phone and carry
// the group suffix on the name. The write is idempotent: re-running over the
// same feed duplicates nothing.
//
// A nil pool means the import is disabled: ImportContacts returns 0, nil
// without touching the network. An empty inboxName still upserts contacts but
// writes no labels or tags.
func ImportContacts(ctx context.Context, pool *pgxpool.Pool, accountID, inboxName string, contacts []session.HistorySyncContact) (int, error) {
	if pool == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%s: %w", opImportContacts, err)
	}
	account, err := strconv.ParseInt(strings.TrimSpace(accountID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid account id %q", opImportContacts, accountID)
	}
	rows := normalizeContacts(contacts)
	if len(rows) == 0 {
		return 0, nil
	}
	tag := strings.TrimSpace(inboxName)
	var tagID int64
	if tag != "" {
		if err := ensureLabel(ctx, pool, account, tag); err != nil {
			return 0, err
		}
		if tagID, err = ensureTag(ctx, pool, tag); err != nil {
			return 0, err
		}
	}
	for _, chunk := range chunkContacts(rows, contactChunkSize) {
		if err := importContactChunk(ctx, pool, account, tagID, tag != "", chunk); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// contactRow is one normalized contact ready for the Chatwoot schema: phone
// is nil when the contact has no dialable number (groups, non-numeric ids)
// so the column stays SQL NULL.
type contactRow struct {
	identifier string
	name       string
	phone      *string
}

// normalizeContacts derives Chatwoot rows from history-sync contacts. The
// JID user part is the identifier; numeric individuals also get an E.164
// phone, groups get NULL phone plus the group suffix on the name, and an
// empty pushname falls back to the identifier. Duplicated identifiers keep
// the last occurrence. Entries without identifier are skipped.
func normalizeContacts(contacts []session.HistorySyncContact) []contactRow {
	rows := make([]contactRow, 0, len(contacts))
	index := make(map[string]int, len(contacts))
	for _, contact := range contacts {
		user, host := splitJID(contact.JID)
		if user == "" {
			continue
		}
		row := contactRow{identifier: user, name: contact.Name}
		if row.name == "" {
			row.name = user
		}
		if host == "g.us" {
			row.name += groupNameSuffix
		} else if isDigits(user) {
			phone := "+" + user
			row.phone = &phone
		}
		if i, dup := index[user]; dup {
			rows[i] = row
			continue
		}
		index[user] = len(rows)
		rows = append(rows, row)
	}
	return rows
}

// splitJID cuts "user@host" into its parts; a bare value is all user.
func splitJID(jid string) (user, host string) {
	if i := strings.LastIndex(jid, "@"); i >= 0 {
		return jid[:i], jid[i+1:]
	}
	return jid, ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func chunkContacts(rows []contactRow, size int) [][]contactRow {
	var chunks [][]contactRow
	for len(rows) > 0 {
		if len(rows) < size {
			size = len(rows)
		}
		chunks = append(chunks, rows[:size])
		rows = rows[size:]
	}
	return chunks
}

// ensureLabel makes the inbox label exist for the account, exactly once.
func ensureLabel(ctx context.Context, pool *pgxpool.Pool, account int64, title string) error {
	const query = `INSERT INTO labels (account_id, title, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (account_id, title) DO NOTHING`
	if _, err := pool.Exec(ctx, query, account, title); err != nil {
		return fmt.Errorf("%s: ensure label: %w", opImportContacts, err)
	}
	return nil
}

// ensureTag makes the inbox tag exist exactly once (single insert, without
// the double-insert the Evolution import performs) and returns its id.
func ensureTag(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	const insertTag = `INSERT INTO tags (name, created_at, updated_at)
		VALUES ($1, NOW(), NOW())
		ON CONFLICT (name) DO NOTHING`
	if _, err := pool.Exec(ctx, insertTag, name); err != nil {
		return 0, fmt.Errorf("%s: ensure tag: %w", opImportContacts, err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM tags WHERE name = $1`, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("%s: lookup tag: %w", opImportContacts, err)
	}
	return id, nil
}

// importContactChunk upserts one chunk of contacts and links their taggings
// in a single transaction. Taggings insert with ON CONFLICT DO NOTHING, so
// re-runs never duplicate them.
func importContactChunk(ctx context.Context, pool *pgxpool.Pool, account, tagID int64, tagged bool, chunk []contactRow) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%s: begin: %w", opImportContacts, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var upsert strings.Builder
	upsert.WriteString(`INSERT INTO contacts (account_id, identifier, name, phone_number, created_at, updated_at) VALUES `)
	args := make([]any, 0, len(chunk)*3+1)
	args = append(args, account)
	for i, row := range chunk {
		if i > 0 {
			upsert.WriteString(", ")
		}
		fmt.Fprintf(&upsert, "($1, $%d, $%d, $%d, NOW(), NOW())", len(args)+1, len(args)+2, len(args)+3)
		args = append(args, row.identifier, row.name, row.phone)
	}
	upsert.WriteString(` ON CONFLICT (identifier, account_id) DO UPDATE
		SET name = EXCLUDED.name, phone_number = EXCLUDED.phone_number, updated_at = NOW()`)
	if _, err := tx.Exec(ctx, upsert.String(), args...); err != nil {
		return fmt.Errorf("%s: upsert contacts: %w", opImportContacts, err)
	}

	identifiers := make([]string, 0, len(chunk))
	for _, row := range chunk {
		identifiers = append(identifiers, row.identifier)
	}
	ids := make(map[string]int64, len(chunk))
	idRows, err := tx.Query(ctx, `SELECT id, identifier FROM contacts WHERE account_id = $1 AND identifier = ANY($2)`, account, identifiers)
	if err != nil {
		return fmt.Errorf("%s: lookup contacts: %w", opImportContacts, err)
	}
	for idRows.Next() {
		var id int64
		var identifier string
		if err := idRows.Scan(&id, &identifier); err != nil {
			idRows.Close()
			return fmt.Errorf("%s: scan contact: %w", opImportContacts, err)
		}
		ids[identifier] = id
	}
	idRows.Close()
	if err := idRows.Err(); err != nil {
		return fmt.Errorf("%s: read contacts: %w", opImportContacts, err)
	}

	if tagged {
		var tagging strings.Builder
		tagging.WriteString(`INSERT INTO taggings (tag_id, taggable_type, taggable_id, context, created_at) VALUES `)
		tagArgs := make([]any, 0, len(chunk)*2+2)
		tagArgs = append(tagArgs, tagID, taggableContact, tagContext)
		n := 0
		for _, row := range chunk {
			id, ok := ids[row.identifier]
			if !ok {
				continue
			}
			if n > 0 {
				tagging.WriteString(", ")
			}
			fmt.Fprintf(&tagging, "($1, $2, $%d, $3, NOW())", len(tagArgs)+1)
			tagArgs = append(tagArgs, id)
			n++
		}
		if n > 0 {
			tagging.WriteString(` ON CONFLICT (tag_id, taggable_type, taggable_id, context) DO NOTHING`)
			if _, err := tx.Exec(ctx, tagging.String(), tagArgs...); err != nil {
				return fmt.Errorf("%s: insert taggings: %w", opImportContacts, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%s: commit: %w", opImportContacts, err)
	}
	return nil
}
