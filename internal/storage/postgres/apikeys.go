package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/storage"
)

// APIKeyRepository is the pgx-backed storage.APIKeyRepository. It reads and
// writes instances.api_key_hash; hashes never leave the method boundary.
type APIKeyRepository struct {
	pool *pgxpool.Pool
}

var _ storage.APIKeyRepository = (*APIKeyRepository)(nil)

// NewAPIKeyRepository returns an API key repository backed by pool.
func NewAPIKeyRepository(pool *pgxpool.Pool) *APIKeyRepository {
	return &APIKeyRepository{pool: pool}
}

// SetHash stores (or replaces) the key hash of an instance.
func (r *APIKeyRepository) SetHash(ctx context.Context, instanceID uuid.UUID, hash string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances
		SET api_key_hash = $2, updated_at = now()
		WHERE id = $1`,
		instanceID, hash,
	)
	if err != nil {
		return fmt.Errorf("set instance key hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set instance key hash: %w", storage.ErrNotFound)
	}
	return nil
}

// InstanceByHash resolves the instance id holding hash or
// storage.ErrNotFound.
func (r *APIKeyRepository) InstanceByHash(ctx context.Context, hash string) (uuid.UUID, error) {
	var id uuid.UUID
	if err := r.pool.QueryRow(ctx,
		`SELECT id FROM instances WHERE api_key_hash = $1`, hash).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("get instance by key hash: %w", storage.ErrNotFound)
		}
		return uuid.Nil, fmt.Errorf("get instance by key hash: %w", err)
	}
	return id, nil
}

// ClearHash revokes the instance key by NULLing its hash.
func (r *APIKeyRepository) ClearHash(ctx context.Context, instanceID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances
		SET api_key_hash = NULL, updated_at = now()
		WHERE id = $1`,
		instanceID,
	)
	if err != nil {
		return fmt.Errorf("clear instance key hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("clear instance key hash: %w", storage.ErrNotFound)
	}
	return nil
}

// CountByOwner counts the instances owned by owner.
func (r *APIKeyRepository) CountByOwner(ctx context.Context, owner uuid.UUID) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM instances WHERE owner_user_id = $1`, owner).Scan(&count); err != nil {
		return 0, fmt.Errorf("count instances by owner: %w", err)
	}
	return count, nil
}

// CountAll counts every instance, any state.
func (r *APIKeyRepository) CountAll(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM instances`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count instances: %w", err)
	}
	return count, nil
}
