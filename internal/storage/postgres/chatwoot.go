package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
)

const chatwootConfigColumns = `instance_id, enabled, url, account_id, token, ` +
	`name_inbox, sign_msg, sign_delimiter, reopen_conversation, conversation_pending, ` +
	`merge_brazil_contacts, import_contacts, import_messages, days_limit, auto_create, ` +
	`organization, logo, ignore_jids, created_at, updated_at`

const chatwootMessageColumns = `instance_id, wa_key, chatwoot_message_id, ` +
	`conversation_id, inbox_id, contact_source_id, is_read, created_at`

// ChatwootConfigRepository is the pgx-backed storage.ChatwootConfigRepository.
type ChatwootConfigRepository struct {
	pool *pgxpool.Pool
}

// ChatwootMessageRepository is the pgx-backed storage.ChatwootMessageRepository.
type ChatwootMessageRepository struct {
	pool *pgxpool.Pool
}

var _ storage.ChatwootConfigRepository = (*ChatwootConfigRepository)(nil)
var _ storage.ChatwootMessageRepository = (*ChatwootMessageRepository)(nil)

// NewChatwootConfigRepository returns a Chatwoot config repository backed by pool.
func NewChatwootConfigRepository(pool *pgxpool.Pool) *ChatwootConfigRepository {
	return &ChatwootConfigRepository{pool: pool}
}

// NewChatwootMessageRepository returns a Chatwoot message repository backed by pool.
func NewChatwootMessageRepository(pool *pgxpool.Pool) *ChatwootMessageRepository {
	return &ChatwootMessageRepository{pool: pool}
}

// NewChatwootRepositories returns the Chatwoot config and message repositories
// backed by the same pool.
func NewChatwootRepositories(pool *pgxpool.Pool) (*ChatwootConfigRepository, *ChatwootMessageRepository) {
	return NewChatwootConfigRepository(pool), NewChatwootMessageRepository(pool)
}

// Get returns the Chatwoot config of an instance or storage.ErrNotFound when
// the instance was never configured.
func (r *ChatwootConfigRepository) Get(ctx context.Context, instanceID uuid.UUID) (*model.ChatwootConfig, error) {
	cfg, err := scanChatwootConfig(r.pool.QueryRow(ctx,
		`SELECT `+chatwootConfigColumns+` FROM chatwoot_configs WHERE instance_id = $1`, instanceID))
	if err != nil {
		return nil, mapChatwootError("get chatwoot config", err)
	}
	return cfg, nil
}

// Put upserts the Chatwoot config of an instance and returns the stored row
// with database timestamps.
func (r *ChatwootConfigRepository) Put(ctx context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error) {
	// pgx encodes a nil slice as NULL, which would violate the
	// ignore_jids NOT NULL constraint; an absent list means "ignore none".
	if cfg.IgnoreJIDs == nil {
		cfg.IgnoreJIDs = []string{}
	}
	stored, err := scanChatwootConfig(r.pool.QueryRow(ctx, `
		INSERT INTO chatwoot_configs (instance_id, enabled, url, account_id, token,
			name_inbox, sign_msg, sign_delimiter, reopen_conversation, conversation_pending,
			merge_brazil_contacts, import_contacts, import_messages, days_limit, auto_create,
			organization, logo, ignore_jids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (instance_id) DO UPDATE SET
			enabled = EXCLUDED.enabled, url = EXCLUDED.url, account_id = EXCLUDED.account_id,
			token = EXCLUDED.token, name_inbox = EXCLUDED.name_inbox, sign_msg = EXCLUDED.sign_msg,
			sign_delimiter = EXCLUDED.sign_delimiter, reopen_conversation = EXCLUDED.reopen_conversation,
			conversation_pending = EXCLUDED.conversation_pending,
			merge_brazil_contacts = EXCLUDED.merge_brazil_contacts,
			import_contacts = EXCLUDED.import_contacts, import_messages = EXCLUDED.import_messages,
			days_limit = EXCLUDED.days_limit, auto_create = EXCLUDED.auto_create,
			organization = EXCLUDED.organization, logo = EXCLUDED.logo,
			ignore_jids = EXCLUDED.ignore_jids, updated_at = now()
		RETURNING `+chatwootConfigColumns,
		cfg.InstanceID, cfg.Enabled, cfg.URL, cfg.AccountID, cfg.Token,
		cfg.NameInbox, cfg.SignMsg, cfg.SignDelimiter, cfg.ReopenConversation, cfg.ConversationPending,
		cfg.MergeBrazilContacts, cfg.ImportContacts, cfg.ImportMessages, cfg.DaysLimit, cfg.AutoCreate,
		cfg.Organization, cfg.Logo, cfg.IgnoreJIDs,
	))
	if err != nil {
		return nil, mapChatwootError("put chatwoot config", err)
	}
	return stored, nil
}

// Delete removes the Chatwoot config of an instance. It reports
// storage.ErrNotFound when no config exists.
func (r *ChatwootConfigRepository) Delete(ctx context.Context, instanceID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chatwoot_configs WHERE instance_id = $1`, instanceID)
	if err != nil {
		return fmt.Errorf("delete chatwoot config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete chatwoot config: %w", storage.ErrNotFound)
	}
	return nil
}

// Put upserts the correlation between a WhatsApp key and its Chatwoot mirror
// and returns the stored row.
func (r *ChatwootMessageRepository) Put(ctx context.Context, msg model.ChatwootMessage) (*model.ChatwootMessage, error) {
	stored, err := scanChatwootMessage(r.pool.QueryRow(ctx, `
		INSERT INTO chatwoot_messages (instance_id, wa_key, chatwoot_message_id,
			conversation_id, inbox_id, contact_source_id, is_read)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (instance_id, wa_key) DO UPDATE SET
			chatwoot_message_id = EXCLUDED.chatwoot_message_id,
			conversation_id = EXCLUDED.conversation_id, inbox_id = EXCLUDED.inbox_id,
			contact_source_id = EXCLUDED.contact_source_id, is_read = EXCLUDED.is_read
		RETURNING `+chatwootMessageColumns,
		msg.InstanceID, msg.WAKey, msg.ChatwootMessageID,
		msg.ConversationID, msg.InboxID, msg.ContactSourceID, msg.IsRead,
	))
	if err != nil {
		return nil, mapChatwootError("put chatwoot message", err)
	}
	return stored, nil
}

// GetByWAKey returns the correlation for a WhatsApp key or
// storage.ErrNotFound.
func (r *ChatwootMessageRepository) GetByWAKey(ctx context.Context, instanceID uuid.UUID, waKey string) (*model.ChatwootMessage, error) {
	msg, err := scanChatwootMessage(r.pool.QueryRow(ctx,
		`SELECT `+chatwootMessageColumns+` FROM chatwoot_messages WHERE instance_id = $1 AND wa_key = $2`,
		instanceID, waKey))
	if err != nil {
		return nil, mapChatwootError("get chatwoot message", err)
	}
	return msg, nil
}

// DeleteByInstance removes every correlation row of an instance and returns
// how many were removed.
func (r *ChatwootMessageRepository) DeleteByInstance(ctx context.Context, instanceID uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chatwoot_messages WHERE instance_id = $1`, instanceID)
	if err != nil {
		return 0, fmt.Errorf("delete chatwoot messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// GetByChatwootID returns the correlation holding a Chatwoot message id or
// storage.ErrNotFound. It backs the inbound quoted and reverse-delete
// lookups without changing the storage interface.
func (r *ChatwootMessageRepository) GetByChatwootID(ctx context.Context, instanceID uuid.UUID, chatwootID int64) (*model.ChatwootMessage, error) {
	msg, err := scanChatwootMessage(r.pool.QueryRow(ctx,
		`SELECT `+chatwootMessageColumns+` FROM chatwoot_messages WHERE instance_id = $1 AND chatwoot_message_id = $2 LIMIT 1`,
		instanceID, chatwootID))
	if err != nil {
		return nil, mapChatwootError("get chatwoot message by chatwoot id", err)
	}
	return msg, nil
}

// LatestByConversation returns the newest correlation of a conversation or
// storage.ErrNotFound. It backs the inbound MESSAGE_READ marking of the last
// received message. The tiebreak on chatwoot_message_id keeps the choice
// deterministic when two rows share created_at (same instant), matching the
// (instance_id, conversation_id, created_at DESC, chatwoot_message_id DESC)
// covering index from migration 00004.
func (r *ChatwootMessageRepository) LatestByConversation(ctx context.Context, instanceID uuid.UUID, conversationID int64) (*model.ChatwootMessage, error) {
	msg, err := scanChatwootMessage(r.pool.QueryRow(ctx,
		`SELECT `+chatwootMessageColumns+` FROM chatwoot_messages WHERE instance_id = $1 AND conversation_id = $2 ORDER BY created_at DESC, chatwoot_message_id DESC LIMIT 1`,
		instanceID, conversationID))
	if err != nil {
		return nil, mapChatwootError("get latest chatwoot message", err)
	}
	return msg, nil
}

func scanChatwootConfig(scanner rowScanner) (*model.ChatwootConfig, error) {
	var cfg model.ChatwootConfig
	if err := scanner.Scan(
		&cfg.InstanceID, &cfg.Enabled, &cfg.URL, &cfg.AccountID, &cfg.Token,
		&cfg.NameInbox, &cfg.SignMsg, &cfg.SignDelimiter, &cfg.ReopenConversation, &cfg.ConversationPending,
		&cfg.MergeBrazilContacts, &cfg.ImportContacts, &cfg.ImportMessages, &cfg.DaysLimit, &cfg.AutoCreate,
		&cfg.Organization, &cfg.Logo, &cfg.IgnoreJIDs,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func scanChatwootMessage(scanner rowScanner) (*model.ChatwootMessage, error) {
	var msg model.ChatwootMessage
	if err := scanner.Scan(
		&msg.InstanceID, &msg.WAKey, &msg.ChatwootMessageID,
		&msg.ConversationID, &msg.InboxID, &msg.ContactSourceID, &msg.IsRead,
		&msg.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &msg, nil
}

func mapChatwootError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}
