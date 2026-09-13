package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
)

const messageColumns = `id, instance_id, type, recipient, payload, media_id, status, ` +
	`COALESCE(whatsapp_id, '') AS whatsapp_id, COALESCE(last_error, '') AS last_error, ` +
	`retries, delivered_at, read_at, created_at, updated_at`

const listMessagesQuery = `SELECT ` + messageColumns + ` FROM message_queue ` +
	`WHERE instance_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`

const listMessagesAfterQuery = `SELECT ` + messageColumns + ` FROM message_queue ` +
	`WHERE instance_id = $1 AND (created_at, id) < (SELECT created_at, id FROM message_queue WHERE id = $2) ` +
	`ORDER BY created_at DESC, id DESC LIMIT $3`

const claimQueuedSelectQuery = `SELECT ` + messageColumns + ` FROM message_queue ` +
	`WHERE status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= now()) ` +
	`ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1`

const claimQueuedUpdateQuery = `UPDATE message_queue SET status = 'sending', updated_at = now() ` +
	`WHERE id = ANY($1) RETURNING ` + messageColumns

// MessageRepository is the pgx-backed storage.MessageRepository.
type MessageRepository struct {
	pool *pgxpool.Pool
}

var _ storage.MessageRepository = (*MessageRepository)(nil)

// NewMessageRepository returns a message repository backed by pool.
func NewMessageRepository(pool *pgxpool.Pool) *MessageRepository {
	return &MessageRepository{pool: pool}
}

// Create persists a new outbound message and returns it with database
// timestamps.
func (r *MessageRepository) Create(ctx context.Context, message model.OutboundMessage) (*model.OutboundMessage, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO message_queue (id, instance_id, type, recipient, payload, media_id, status, whatsapp_id)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7, ''), 'queued'), NULLIF($8, ''))
		RETURNING `+messageColumns,
		message.ID, message.InstanceID, message.Type, message.RecipientJID,
		message.Payload, message.MediaID, message.Status, message.WhatsAppMessageID,
	)

	created, err := scanMessage(row)
	if err != nil {
		return nil, mapMessageError("create message", err)
	}
	return created, nil
}

// Get returns the message with the given id or storage.ErrNotFound.
func (r *MessageRepository) Get(ctx context.Context, id uuid.UUID) (*model.OutboundMessage, error) {
	message, err := scanMessage(r.pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM message_queue WHERE id = $1`, id))
	if err != nil {
		return nil, mapMessageError("get message", err)
	}
	return message, nil
}

// ListByInstance returns up to limit messages of instanceID ordered by
// created_at descending and the cursor to fetch the next page.
func (r *MessageRepository) ListByInstance(
	ctx context.Context, instanceID uuid.UUID, limit int, cursor string,
) ([]model.OutboundMessage, string, error) {
	if limit <= 0 {
		return []model.OutboundMessage{}, "", nil
	}

	query := listMessagesQuery
	args := []any{instanceID, limit + 1}
	if cursor != "" {
		cursorID, err := uuid.Parse(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("list messages: %w", storage.ErrInvalidCursor)
		}
		query = listMessagesAfterQuery
		args = []any{instanceID, cursorID, limit + 1}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	messages := []model.OutboundMessage{}
	for rows.Next() {
		var message model.OutboundMessage
		if err := scanMessageRow(rows, &message); err != nil {
			return nil, "", fmt.Errorf("list messages: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list messages: %w", err)
	}

	nextCursor := ""
	if len(messages) > limit {
		messages = messages[:limit]
		nextCursor = messages[limit-1].ID.String()
	}
	return messages, nextCursor, nil
}

// ClaimQueued atomically moves up to limit due queued messages to sending,
// oldest first, skipping rows locked by other transactions.
func (r *MessageRepository) ClaimQueued(ctx context.Context, limit int) ([]model.OutboundMessage, error) {
	if limit <= 0 {
		return []model.OutboundMessage{}, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim queued messages: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, claimQueuedSelectQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("claim queued messages: select: %w", err)
	}

	ids := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var message model.OutboundMessage
		if err := scanMessageRow(rows, &message); err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim queued messages: %w", err)
		}
		ids = append(ids, message.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim queued messages: %w", err)
	}

	if len(ids) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("claim queued messages: commit: %w", err)
		}
		return []model.OutboundMessage{}, nil
	}

	updated, err := tx.Query(ctx, claimQueuedUpdateQuery, ids)
	if err != nil {
		return nil, fmt.Errorf("claim queued messages: update: %w", err)
	}

	claimedByID := make(map[uuid.UUID]model.OutboundMessage, len(ids))
	for updated.Next() {
		message, err := scanMessage(updated)
		if err != nil {
			updated.Close()
			return nil, fmt.Errorf("claim queued messages: %w", err)
		}
		claimedByID[message.ID] = *message
	}
	updated.Close()
	if err := updated.Err(); err != nil {
		return nil, fmt.Errorf("claim queued messages: %w", err)
	}

	claimed := make([]model.OutboundMessage, 0, len(ids))
	for _, id := range ids {
		message, ok := claimedByID[id]
		if !ok {
			return nil, fmt.Errorf("claim queued messages: message %s missing from update", id)
		}
		claimed = append(claimed, message)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim queued messages: commit: %w", err)
	}
	return claimed, nil
}

// MarkSent marks the message as sent with its WhatsApp identifier.
func (r *MessageRepository) MarkSent(ctx context.Context, id uuid.UUID, whatsAppMessageID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE message_queue
		SET status = 'sent', whatsapp_id = NULLIF($2, ''), last_error = NULL, updated_at = now()
		WHERE id = $1`, id, whatsAppMessageID)
	if err != nil {
		return fmt.Errorf("mark message sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark message sent: %w", storage.ErrNotFound)
	}
	return nil
}

// MarkFailed marks the message as failed with the definitive reason.
func (r *MessageRepository) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE message_queue
		SET status = 'failed', last_error = NULLIF($2, ''), updated_at = now()
		WHERE id = $1`, id, errMsg)
	if err != nil {
		return fmt.Errorf("mark message failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark message failed: %w", storage.ErrNotFound)
	}
	return nil
}

// MarkRetrying moves the message back to queued for a later attempt.
func (r *MessageRepository) MarkRetrying(ctx context.Context, id uuid.UUID, errMsg string, nextAttemptAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE message_queue
		SET status = 'queued', retries = retries + 1, last_error = NULLIF($2, ''),
		    next_attempt_at = $3, updated_at = now()
		WHERE id = $1`, id, errMsg, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("mark message retrying: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark message retrying: %w", storage.ErrNotFound)
	}
	return nil
}

// UpdateReceipt records a delivery or read milestone by WhatsApp message id.
func (r *MessageRepository) UpdateReceipt(
	ctx context.Context, whatsAppMessageID, status string, at time.Time,
) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE message_queue
		SET delivered_at = COALESCE(delivered_at, $3),
		    read_at = CASE WHEN $2 IN ('read', 'played') THEN COALESCE(read_at, $3) ELSE read_at END,
		    updated_at = now()
		WHERE whatsapp_id = $1 AND $2 IN ('delivered', 'read', 'played')`,
		whatsAppMessageID, status, at)
	if err != nil {
		return false, fmt.Errorf("update message receipt: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RequeueStuck moves sending messages last updated before olderThan back to
// queued and returns how many were recovered.
func (r *MessageRepository) RequeueStuck(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE message_queue
		SET status = 'queued', updated_at = now()
		WHERE status = 'sending' AND updated_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("requeue stuck messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanMessage(scanner rowScanner) (*model.OutboundMessage, error) {
	var message model.OutboundMessage
	if err := scanMessageRow(scanner, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func scanMessageRow(scanner rowScanner, message *model.OutboundMessage) error {
	return scanner.Scan(
		&message.ID, &message.InstanceID, &message.Type, &message.RecipientJID,
		&message.Payload, &message.MediaID, &message.Status, &message.WhatsAppMessageID,
		&message.LastError, &message.Attempts, &message.DeliveredAt, &message.ReadAt,
		&message.CreatedAt, &message.UpdatedAt,
	)
}

func mapMessageError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}
