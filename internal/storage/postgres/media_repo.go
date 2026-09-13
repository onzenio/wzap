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

const mediaColumns = `id, instance_id, direction, COALESCE(message_id, '') AS message_id, ` +
	`mimetype, COALESCE(filename, '') AS filename, size_bytes, storage_path, sha256, created_at, expires_at`

// MediaRepository is the pgx-backed storage.MediaRepository.
type MediaRepository struct {
	pool *pgxpool.Pool
}

var _ storage.MediaRepository = (*MediaRepository)(nil)

// NewMediaRepository returns a media repository backed by pool.
func NewMediaRepository(pool *pgxpool.Pool) *MediaRepository {
	return &MediaRepository{pool: pool}
}

// Create persists a new media row and returns it with the database created_at.
func (r *MediaRepository) Create(ctx context.Context, media model.Media) (*model.Media, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO media (id, instance_id, direction, message_id, mimetype, filename,
		                   size_bytes, storage_path, sha256, expires_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, $9, $10)
		RETURNING `+mediaColumns,
		media.ID, media.InstanceID, media.Direction, media.MessageID, media.Mimetype,
		media.Filename, media.SizeBytes, media.StoragePath, media.SHA256,
		media.ExpiresAt,
	)

	created, err := scanMedia(row)
	if err != nil {
		return nil, mapMediaError("create media", err)
	}
	return created, nil
}

// Get returns the media with the given id or storage.ErrNotFound.
func (r *MediaRepository) Get(ctx context.Context, id uuid.UUID) (*model.Media, error) {
	media, err := scanMedia(r.pool.QueryRow(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE id = $1`, id))
	if err != nil {
		return nil, mapMediaError("get media", err)
	}
	return media, nil
}

// ListByInstance returns every media of instanceID ordered by created_at.
func (r *MediaRepository) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]model.Media, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE instance_id = $1 ORDER BY created_at, id`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	defer rows.Close()

	return scanMediaRows(rows)
}

// ListExpired returns the media whose expiry is due, oldest first.
func (r *MediaRepository) ListExpired(ctx context.Context, now time.Time) ([]model.Media, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+mediaColumns+` FROM media WHERE expires_at <= $1 ORDER BY expires_at, id`, now)
	if err != nil {
		return nil, fmt.Errorf("list expired media: %w", err)
	}
	defer rows.Close()

	return scanMediaRows(rows)
}

// Delete removes one media row or returns storage.ErrNotFound.
func (r *MediaRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM media WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete media: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete media: %w", storage.ErrNotFound)
	}
	return nil
}

// DeleteByInstance removes every media row of instanceID and returns how many
// were removed.
func (r *MediaRepository) DeleteByInstance(ctx context.Context, instanceID uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM media WHERE instance_id = $1`, instanceID)
	if err != nil {
		return 0, fmt.Errorf("delete instance media: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanMediaRows(rows pgx.Rows) ([]model.Media, error) {
	records := []model.Media{}
	for rows.Next() {
		var record model.Media
		if err := scanMediaRow(rows, &record); err != nil {
			return nil, fmt.Errorf("scan media: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan media: %w", err)
	}
	return records, nil
}

func scanMedia(scanner rowScanner) (*model.Media, error) {
	var record model.Media
	if err := scanMediaRow(scanner, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func scanMediaRow(scanner rowScanner, record *model.Media) error {
	return scanner.Scan(
		&record.ID, &record.InstanceID, &record.Direction, &record.MessageID,
		&record.Mimetype, &record.Filename, &record.SizeBytes, &record.StoragePath,
		&record.SHA256, &record.CreatedAt, &record.ExpiresAt,
	)
}

// mapMediaError translates the pgx sentinels into storage ones. A foreign key
// violation means the instance does not exist.
func mapMediaError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}
