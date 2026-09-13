package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/storage"
)

// JIDCacheRepository is the pgx-backed storage.JIDCacheRepository.
type JIDCacheRepository struct {
	pool *pgxpool.Pool
}

var _ storage.JIDCacheRepository = (*JIDCacheRepository)(nil)

// NewJIDCacheRepository returns a JID cache repository backed by pool.
func NewJIDCacheRepository(pool *pgxpool.Pool) *JIDCacheRepository {
	return &JIDCacheRepository{pool: pool}
}

// Get returns the cached JID for phone. Expired entries count as a miss but are
// left in place for DeleteExpired to clean up.
func (r *JIDCacheRepository) Get(ctx context.Context, phone string) (string, bool, error) {
	var jid string
	err := r.pool.QueryRow(ctx,
		`SELECT jid FROM contacts WHERE phone = $1 AND expires_at > now()`, phone).Scan(&jid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get jid cache: %w", err)
	}
	return jid, true, nil
}

// Put upserts the JID resolved for phone so refreshes replace stale entries.
func (r *JIDCacheRepository) Put(ctx context.Context, phone, jid string, expiresAt time.Time) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO contacts (phone, jid, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (phone) DO UPDATE SET jid = EXCLUDED.jid, expires_at = EXCLUDED.expires_at`,
		phone, jid, expiresAt); err != nil {
		return fmt.Errorf("put jid cache: %w", err)
	}
	return nil
}

// DeleteExpired removes entries whose expiry is in the past and returns how
// many were removed.
func (r *JIDCacheRepository) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired jid cache: %w", err)
	}
	return tag.RowsAffected(), nil
}
