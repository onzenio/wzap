package chatimport

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/session"
)

const (
	opImportContacts = "chatimport: import contacts"
	opImportMessages = "chatimport: import messages"
	// contactChunkSize bounds each multi-row upsert; mirrors the Evolution
	// import batching.
	contactChunkSize = 3000
	// messageChunkSize bounds each multi-row message insert; the Evolution
	// import batches the same way.
	messageChunkSize = 500
	// taggableContact and tagContext are the acts-as-taggable-on coordinates
	// Chatwoot uses for contact tags.
	taggableContact = "Contact"
	tagContext      = "tags"
	// groupNameSuffix marks imported group chats, which carry no phone.
	groupNameSuffix = " (GROUP)"
	// importPlaceholderContent replaces content-less history messages when
	// placeholders are configured; without it they are skipped.
	importPlaceholderContent = "(mídia não importada)"
	// messageSourcePrefix namespaces every imported Chatwoot message back to
	// its WhatsApp key, the same namespace the live mirror uses, so a
	// history re-import dedups rows the mirror already wrote.
	messageSourcePrefix = "WAID:"
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

// MessagesDeps carries the inputs of ImportMessages. Pool nil means the
// import is disabled (0, nil, no network). AccountID is the Chatwoot account
// in string form; Token resolves the outgoing sender through access_tokens
// (empty leaves outgoing senders NULL). DaysLimit cuts history older than
// that many days (0 disables the cut); NewerThan cuts everything older than
// the instant (zero disables, used by the lost-messages cron window).
// Placeholder turns content-less messages into the placeholder text instead
// of skipping them.
type MessagesDeps struct {
	Pool        *pgxpool.Pool
	AccountID   string
	Token       string
	DaysLimit   int
	NewerThan   time.Time
	Placeholder bool
}

// ImportMessages inserts history-sync messages into the Chatwoot database,
// returning how many were written. Rows land ordered by phone+time, dedup by
// pre-existing source_id (WAID:, shared with the live mirror, so mirror rows
// dedup history and re-runs import nothing), in batches. FromMe maps to
// message_type 1 with the access_tokens user as sender, everything else to
// message_type 0 with the contact as sender. Messages without content are
// skipped unless Placeholder is set; messages without chat or key are always
// skipped. Message authors missing from contacts are ensured on demand
// through the same chunk writer as the bulk contacts import (untagged: no
// inbox name reaches MessagesDeps), so a messages-only run imports instead
// of silently dropping every row; the ImportContacts flag keeps meaning the
// tagged history backfill.
//
// A nil pool means the import is disabled: ImportMessages returns 0, nil
// without touching the network.
func ImportMessages(ctx context.Context, deps MessagesDeps, inboxID int64, msgs []session.HistorySyncMessage) (int, error) {
	if deps.Pool == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%s: %w", opImportMessages, err)
	}
	account, err := strconv.ParseInt(strings.TrimSpace(deps.AccountID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid account id %q", opImportMessages, deps.AccountID)
	}
	rows := normalizeMessages(msgs, deps.DaysLimit, deps.NewerThan, deps.Placeholder)
	if len(rows) == 0 {
		return 0, nil
	}
	var senderUser *int64
	if token := strings.TrimSpace(deps.Token); token != "" {
		id, found, err := lookupTokenOwner(ctx, deps.Pool, token)
		if err != nil {
			return 0, err
		}
		if found {
			senderUser = &id
		}
	}
	total := 0
	for _, chunk := range chunkMessageRows(rows, messageChunkSize) {
		n, err := importMessageChunk(ctx, deps.Pool, account, inboxID, senderUser, chunk)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// messageRow is one normalized history message ready for the Chatwoot
// schema: identifier is the chat user part (phone or group id), host the JID
// domain (g.us marks group chats for the on-demand contact ensure),
// sourceID the WAID: namespaced key, at the instant stored via to_timestamp.
type messageRow struct {
	identifier string
	host       string
	sourceID   string
	content    string
	fromMe     bool
	at         time.Time
}

// normalizeMessages derives Chatwoot rows from history-sync messages: it
// drops key-less and chat-less entries, cuts daysLimit/newerThan windows,
// skips content-less messages unless placeholder is set, stamps timeless
// messages with the import instant, and sorts by phone+time.
func normalizeMessages(msgs []session.HistorySyncMessage, daysLimit int, newerThan time.Time, placeholder bool) []messageRow {
	now := time.Now().UTC()
	var cutoff time.Time
	if daysLimit > 0 {
		cutoff = now.AddDate(0, 0, -daysLimit)
	}
	rows := make([]messageRow, 0, len(msgs))
	for _, msg := range msgs {
		key := strings.TrimSpace(msg.MessageID)
		if key == "" {
			continue
		}
		identifier, host := splitJID(msg.ChatJID)
		if identifier == "" {
			continue
		}
		if !msg.Timestamp.IsZero() {
			if !cutoff.IsZero() && msg.Timestamp.Before(cutoff) {
				continue
			}
			if !newerThan.IsZero() && msg.Timestamp.Before(newerThan) {
				continue
			}
		}
		content := strings.TrimSpace(msg.Text)
		if content == "" {
			if !placeholder {
				continue
			}
			content = importPlaceholderContent
		}
		at := msg.Timestamp
		if at.IsZero() {
			at = now
		}
		rows = append(rows, messageRow{
			identifier: identifier,
			host:       host,
			sourceID:   messageSourcePrefix + key,
			content:    content,
			fromMe:     msg.IsFromMe,
			at:         at,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].identifier != rows[j].identifier {
			return rows[i].identifier < rows[j].identifier
		}
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		return rows[i].sourceID < rows[j].sourceID
	})
	return rows
}

func chunkMessageRows(rows []messageRow, size int) [][]messageRow {
	var chunks [][]messageRow
	for len(rows) > 0 {
		if len(rows) < size {
			size = len(rows)
		}
		chunks = append(chunks, rows[:size])
		rows = rows[size:]
	}
	return chunks
}

// lookupTokenOwner resolves the Chatwoot user owning token through
// access_tokens. Found false means no such token; the import still proceeds
// with NULL outgoing senders.
func lookupTokenOwner(ctx context.Context, pool *pgxpool.Pool, token string) (int64, bool, error) {
	var owner int64
	err := pool.QueryRow(ctx, `SELECT owner_id FROM access_tokens WHERE token = $1 AND owner_type = 'User' LIMIT 1`, token).Scan(&owner)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("%s: lookup sender: %w", opImportMessages, err)
	}
	return owner, true, nil
}

// importMessageChunk writes one chunk of messages: it drops rows whose
// source_id already exists (history or live-mirror writes), ensures the
// authors missing from contacts on demand, ensures one conversation per
// phone, and bulk-inserts the rest with to_timestamp epochs.
func importMessageChunk(ctx context.Context, pool *pgxpool.Pool, account, inboxID int64, senderUser *int64, chunk []messageRow) (int, error) {
	sources := make([]string, 0, len(chunk))
	seen := make(map[string]struct{}, len(chunk))
	for _, row := range chunk {
		if _, dup := seen[row.sourceID]; dup {
			continue
		}
		seen[row.sourceID] = struct{}{}
		sources = append(sources, row.sourceID)
	}
	existing := make(map[string]struct{}, len(sources))
	if len(sources) > 0 {
		rows, err := pool.Query(ctx, `SELECT source_id FROM messages WHERE account_id = $1 AND source_id = ANY($2)`, account, sources)
		if err != nil {
			return 0, fmt.Errorf("%s: lookup existing: %w", opImportMessages, err)
		}
		for rows.Next() {
			var source string
			if err := rows.Scan(&source); err != nil {
				rows.Close()
				return 0, fmt.Errorf("%s: scan existing: %w", opImportMessages, err)
			}
			existing[source] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("%s: read existing: %w", opImportMessages, err)
		}
	}

	identifiers := make([]string, 0, len(chunk))
	identifierSeen := make(map[string]struct{}, len(chunk))
	for _, row := range chunk {
		if _, dup := existing[row.sourceID]; dup {
			continue
		}
		if _, dup := identifierSeen[row.identifier]; dup {
			continue
		}
		identifierSeen[row.identifier] = struct{}{}
		identifiers = append(identifiers, row.identifier)
	}
	contacts := make(map[string]int64, len(identifiers))
	if len(identifiers) > 0 {
		var err error
		contacts, err = queryContactIDs(ctx, pool, opImportMessages, account, identifiers)
		if err != nil {
			return 0, err
		}
	}
	if err := ensureMessageContacts(ctx, pool, account, chunk, existing, contacts); err != nil {
		return 0, err
	}

	conversations := make(map[string]int64, len(contacts))
	for identifier, contactID := range contacts {
		convID, err := ensureConversation(ctx, pool, account, inboxID, contactID)
		if err != nil {
			return 0, err
		}
		conversations[identifier] = convID
	}

	// fresh keeps the first occurrence of each new source_id in chunk order.
	fresh := make([]messageRow, 0, len(chunk))
	freshSeen := make(map[string]struct{}, len(chunk))
	for _, row := range chunk {
		if _, dup := existing[row.sourceID]; dup {
			continue
		}
		if _, dup := freshSeen[row.sourceID]; dup {
			continue
		}
		freshSeen[row.sourceID] = struct{}{}
		if _, ok := conversations[row.identifier]; !ok {
			continue
		}
		fresh = append(fresh, row)
	}
	if len(fresh) == 0 {
		return 0, nil
	}

	var insert strings.Builder
	insert.WriteString(`INSERT INTO messages (content, account_id, inbox_id, conversation_id, message_type, created_at, updated_at, private, status, source_id, content_type, content_attributes, sender_type, sender_id) VALUES `)
	args := make([]any, 0, len(fresh)*8)
	for i, row := range fresh {
		if i > 0 {
			insert.WriteString(", ")
		}
		convID := conversations[row.identifier]
		msgType := 0
		senderType := "Contact"
		var senderID any = contacts[row.identifier]
		if row.fromMe {
			msgType = 1
			senderType = "User"
			senderID = nil
			if senderUser != nil {
				senderID = *senderUser
			}
		}
		epoch := float64(row.at.UnixNano()) / 1e9
		base := len(args) + 1
		// Explicit ordinal binds keep the bulk insert a single round-trip;
		// to_timestamp adapts the Evolution epoch handling.
		fmt.Fprintf(&insert, "($%d, $%d, $%d, $%d, $%d, to_timestamp($%d), to_timestamp($%d), FALSE, 0, $%d, 0, '{}', $%d, $%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9)
		args = append(args, row.content, account, inboxID, convID, msgType, epoch, epoch, row.sourceID, senderType, senderID)
	}
	if _, err := pool.Exec(ctx, insert.String(), args...); err != nil {
		return 0, fmt.Errorf("%s: insert messages: %w", opImportMessages, err)
	}
	return len(fresh), nil
}

// queryContactIDs maps contact identifiers to ids for one account.
func queryContactIDs(ctx context.Context, pool *pgxpool.Pool, op string, account int64, identifiers []string) (map[string]int64, error) {
	contacts := make(map[string]int64, len(identifiers))
	rows, err := pool.Query(ctx, `SELECT id, identifier FROM contacts WHERE account_id = $1 AND identifier = ANY($2)`, account, identifiers)
	if err != nil {
		return nil, fmt.Errorf("%s: lookup contacts: %w", op, err)
	}
	for rows.Next() {
		var id int64
		var identifier string
		if err := rows.Scan(&id, &identifier); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%s: scan contact: %w", op, err)
		}
		contacts[identifier] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: read contacts: %w", op, err)
	}
	return contacts, nil
}

// ensureMessageContacts upserts the authors the chunk needs but contacts
// lacks, reusing the bulk contacts chunk writer untagged (no inbox tag
// reaches MessagesDeps). Without it a messages-only run (ImportContacts
// off) would silently drop every row for want of a conversation home.
func ensureMessageContacts(ctx context.Context, pool *pgxpool.Pool, account int64, chunk []messageRow, existing map[string]struct{}, contacts map[string]int64) error {
	hosts := make(map[string]string, len(chunk))
	for _, row := range chunk {
		if _, dup := existing[row.sourceID]; dup {
			continue
		}
		if _, seen := hosts[row.identifier]; !seen {
			hosts[row.identifier] = row.host
		}
	}
	var missing []contactRow
	for identifier, host := range hosts {
		if _, ok := contacts[identifier]; ok {
			continue
		}
		row := contactRow{identifier: identifier, name: identifier}
		if host == "g.us" {
			row.name += groupNameSuffix
		} else if isDigits(identifier) {
			phone := "+" + identifier
			row.phone = &phone
		}
		missing = append(missing, row)
	}
	if len(missing) == 0 {
		return nil
	}
	// Deterministic row order: concurrent triggers upsert overlapping
	// authors, and a shared lock order keeps those upserts from deadlocking
	// on the (identifier, account_id) key.
	sort.Slice(missing, func(i, j int) bool { return missing[i].identifier < missing[j].identifier })
	if err := importContactChunk(ctx, pool, account, 0, false, missing); err != nil {
		return err
	}
	ids := make([]string, 0, len(missing))
	for _, row := range missing {
		ids = append(ids, row.identifier)
	}
	ensured, err := queryContactIDs(ctx, pool, opImportMessages, account, ids)
	if err != nil {
		return err
	}
	for identifier, id := range ensured {
		contacts[identifier] = id
	}
	return nil
}

// conversationMu serializes conversation creation across concurrent import
// triggers (manual, auto, cron). display_id allocates via MAX()+1 against
// UNIQUE(account_id, display_id), so two triggers racing the lookup would
// insert the same id and violate the constraint. One replica is the
// supported runtime, so a process mutex is the smallest correct lock; the
// live mirror writes through the Chatwoot API, never this SQL path, so no
// outside writer can interleave. The whole ensure is covered, not just the
// allocation: the same race would otherwise duplicate the contact_inbox link
// and the conversation itself.
var conversationMu sync.Mutex

// ensureConversation returns the conversation of (account, inbox, contact),
// creating the contact_inbox link and the conversation when absent. The
// display_id follows the per-account sequence Chatwoot expects.
func ensureConversation(ctx context.Context, pool *pgxpool.Pool, account, inboxID, contactID int64) (int64, error) {
	conversationMu.Lock()
	defer conversationMu.Unlock()
	var convID int64
	err := pool.QueryRow(ctx, `SELECT id FROM conversations WHERE account_id = $1 AND inbox_id = $2 AND contact_id = $3 ORDER BY id LIMIT 1`,
		account, inboxID, contactID).Scan(&convID)
	if err == nil {
		return convID, nil
	}
	if err != pgx.ErrNoRows {
		return 0, fmt.Errorf("%s: lookup conversation: %w", opImportMessages, err)
	}
	var inboxLinkID int64
	err = pool.QueryRow(ctx, `SELECT id FROM contact_inboxes WHERE contact_id = $1 AND inbox_id = $2 ORDER BY id LIMIT 1`,
		contactID, inboxID).Scan(&inboxLinkID)
	if err == pgx.ErrNoRows {
		source := uuid.NewString()
		err = pool.QueryRow(ctx, `INSERT INTO contact_inboxes (contact_id, inbox_id, source_id, created_at, updated_at)
			VALUES ($1, $2, $3, NOW(), NOW()) RETURNING id`, contactID, inboxID, source).Scan(&inboxLinkID)
		if err != nil {
			return 0, fmt.Errorf("%s: ensure contact_inbox: %w", opImportMessages, err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("%s: lookup contact_inbox: %w", opImportMessages, err)
	}
	var display int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(display_id), 0) + 1 FROM conversations WHERE account_id = $1`, account).Scan(&display); err != nil {
		return 0, fmt.Errorf("%s: next display id: %w", opImportMessages, err)
	}
	err = pool.QueryRow(ctx, `INSERT INTO conversations (account_id, inbox_id, status, contact_id, contact_inbox_id, display_id, created_at, updated_at, last_activity_at)
		VALUES ($1, $2, 0, $3, $4, $5, NOW(), NOW(), NOW()) RETURNING id`,
		account, inboxID, contactID, inboxLinkID, display).Scan(&convID)
	if err != nil {
		return 0, fmt.Errorf("%s: ensure conversation: %w", opImportMessages, err)
	}
	return convID, nil
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
