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

const (
	idempotencyStatusInProgress = "in_progress"
	idempotencyStatusCompleted  = "completed"
)

const idempotencyColumns = `instance_id, idempotency_key, request_hash, status, ` +
	`COALESCE(response_status, 0) AS response_status, response_body, created_at, expires_at`

// IdempotencyRepository is the pgx-backed storage.IdempotencyRepository.
type IdempotencyRepository struct {
	pool *pgxpool.Pool
}

var _ storage.IdempotencyRepository = (*IdempotencyRepository)(nil)

// NewIdempotencyRepository returns an idempotency repository backed by pool.
func NewIdempotencyRepository(pool *pgxpool.Pool) *IdempotencyRepository {
	return &IdempotencyRepository{pool: pool}
}

// Acquire relies on the (instance_id, key) primary key to settle the race: two
// concurrent callers both run the INSERT and only one affects a row. The
// conflict path re-reads the stored record instead of running a SELECT first,
// which would let both callers through.
func (r *IdempotencyRepository) Acquire(
	ctx context.Context, instanceID uuid.UUID, key, fingerprint string, expiresAt time.Time,
) (*model.IdempotencyRecord, bool, error) {
	// An expired key is as good as absent, so drop it before trying to own it.
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE instance_id = $1 AND idempotency_key = $2 AND expires_at < now()`,
		instanceID, key); err != nil {
		return nil, false, fmt.Errorf("acquire idempotency key: drop expired: %w", err)
	}

	inserted, err := scanIdempotencyRecord(r.pool.QueryRow(ctx, `
		INSERT INTO idempotency_keys (instance_id, idempotency_key, request_hash, status, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (instance_id, idempotency_key) DO NOTHING
		RETURNING `+idempotencyColumns,
		instanceID, key, fingerprint, idempotencyStatusInProgress, expiresAt))
	if err == nil {
		return inserted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, mapIdempotencyError("acquire idempotency key", err)
	}

	existing, err := scanIdempotencyRecord(r.pool.QueryRow(ctx,
		`SELECT `+idempotencyColumns+` FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`,
		instanceID, key))
	if err != nil {
		return nil, false, mapIdempotencyError("acquire idempotency key", err)
	}
	if existing.Fingerprint != fingerprint {
		return nil, false, fmt.Errorf("acquire idempotency key: %w", storage.ErrFingerprintMismatch)
	}
	if existing.Status != idempotencyStatusCompleted {
		return nil, false, fmt.Errorf("acquire idempotency key: %w", storage.ErrInProgress)
	}
	return existing, false, nil
}

// Complete stores the original response under key.
func (r *IdempotencyRepository) Complete(
	ctx context.Context, instanceID uuid.UUID, key string, status int, body []byte,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = $3, response_status = $4, response_body = $5
		WHERE instance_id = $1 AND idempotency_key = $2`,
		instanceID, key, idempotencyStatusCompleted, status, body)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("complete idempotency key: %w", storage.ErrNotFound)
	}
	return nil
}

// Release frees key for a corrected retry. Releasing a key that is already
// gone is not an error, so deferred releases are safe.
func (r *IdempotencyRepository) Release(ctx context.Context, instanceID uuid.UUID, key string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`,
		instanceID, key); err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

// DeleteExpired removes keys whose expiry is in the past and returns how many
// were removed.
func (r *IdempotencyRepository) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanIdempotencyRecord(scanner rowScanner) (*model.IdempotencyRecord, error) {
	var record model.IdempotencyRecord
	if err := scanner.Scan(
		&record.InstanceID, &record.Key, &record.Fingerprint, &record.Status,
		&record.ResponseStatus, &record.ResponseBody, &record.CreatedAt, &record.ExpiresAt,
	); err != nil {
		return nil, err
	}
	return &record, nil
}

func mapIdempotencyError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}
