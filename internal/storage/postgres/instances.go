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

const instanceColumns = `id, name, COALESCE(external_ref, '') AS external_ref, status, ` +
	`COALESCE(whatsapp_jid, '') AS whatsapp_jid, last_connected_at, COALESCE(last_error, '') AS last_error, ` +
	`owner_user_id, webhook_url, webhook_enabled, webhook_events, ` +
	`created_at, updated_at`

const listInstancesQuery = `SELECT ` + instanceColumns + ` FROM instances ORDER BY created_at DESC, id DESC LIMIT $1`

const listInstancesAfterQuery = `SELECT ` + instanceColumns + ` FROM instances ` +
	`WHERE (created_at, id) < (SELECT created_at, id FROM instances WHERE id = $2) ` +
	`ORDER BY created_at DESC, id DESC LIMIT $1`

// InstanceRepository is the pgx-backed storage.InstanceRepository.
type InstanceRepository struct {
	pool *pgxpool.Pool
}

var _ storage.InstanceRepository = (*InstanceRepository)(nil)

// NewInstanceRepository returns an instance repository backed by pool.
func NewInstanceRepository(pool *pgxpool.Pool) *InstanceRepository {
	return &InstanceRepository{pool: pool}
}

// Create persists a new instance and returns it with database timestamps.
func (r *InstanceRepository) Create(ctx context.Context, instance model.Instance) (*model.Instance, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO instances (id, name, external_ref, status, whatsapp_jid, last_connected_at, last_error)
		VALUES ($1, $2, NULLIF($3, ''), COALESCE(NULLIF($4, ''), 'disconnected'), NULLIF($5, ''), $6, NULLIF($7, ''))
		RETURNING `+instanceColumns,
		instance.ID, instance.Name, instance.ExternalRef, instance.Status,
		instance.WhatsAppJID, instance.LastConnectedAt, instance.LastError,
	)

	created, err := scanInstance(row)
	if err != nil {
		return nil, mapInstanceError("create instance", err)
	}
	return created, nil
}

// Get returns the instance with the given id or storage.ErrNotFound.
func (r *InstanceRepository) Get(ctx context.Context, id uuid.UUID) (*model.Instance, error) {
	instance, err := scanInstance(r.pool.QueryRow(ctx, `SELECT `+instanceColumns+` FROM instances WHERE id = $1`, id))
	if err != nil {
		return nil, mapInstanceError("get instance", err)
	}
	return instance, nil
}

// GetByExternalRef returns the instance with the given external_ref or
// storage.ErrNotFound.
func (r *InstanceRepository) GetByExternalRef(ctx context.Context, externalRef string) (*model.Instance, error) {
	instance, err := scanInstance(r.pool.QueryRow(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE external_ref = $1`, externalRef))
	if err != nil {
		return nil, mapInstanceError("get instance by external ref", err)
	}
	return instance, nil
}

// List returns up to limit instances ordered by created_at descending and the
// cursor to fetch the next page.
func (r *InstanceRepository) List(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error) {
	if limit <= 0 {
		return []model.Instance{}, "", nil
	}

	query := listInstancesQuery
	args := []any{limit + 1}
	if cursor != "" {
		cursorID, err := uuid.Parse(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("list instances: %w", storage.ErrInvalidCursor)
		}
		query = listInstancesAfterQuery
		args = append(args, cursorID)
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()

	instances := []model.Instance{}
	for rows.Next() {
		var instance model.Instance
		if err := scanInstanceRow(rows, &instance); err != nil {
			return nil, "", fmt.Errorf("list instances: %w", err)
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list instances: %w", err)
	}

	nextCursor := ""
	if len(instances) > limit {
		instances = instances[:limit]
		nextCursor = instances[limit-1].ID.String()
	}
	return instances, nextCursor, nil
}

// Update persists the mutable fields of instance and returns the stored row.
func (r *InstanceRepository) Update(ctx context.Context, instance model.Instance) (*model.Instance, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE instances
		SET name = $2, external_ref = NULLIF($3, ''), status = COALESCE(NULLIF($4, ''), status),
		    whatsapp_jid = NULLIF($5, ''), last_connected_at = $6, last_error = NULLIF($7, ''), updated_at = now()
		WHERE id = $1
		RETURNING `+instanceColumns,
		instance.ID, instance.Name, instance.ExternalRef, instance.Status,
		instance.WhatsAppJID, instance.LastConnectedAt, instance.LastError,
	)

	updated, err := scanInstance(row)
	if err != nil {
		return nil, mapInstanceError("update instance", err)
	}
	return updated, nil
}

// SetConnection updates the connection columns of an instance and clears the
// stored JID when whatsappJID is empty. It leaves the other columns untouched.
func (r *InstanceRepository) SetConnection(ctx context.Context, id uuid.UUID, status, whatsappJID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances
		SET status = $2, whatsapp_jid = NULLIF($3, ''), updated_at = now()
		WHERE id = $1`,
		id, status, whatsappJID,
	)
	if err != nil {
		return fmt.Errorf("set instance connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set instance connection: %w", storage.ErrNotFound)
	}
	return nil
}

// SetConnectionState records a connection transition on an instance without
// touching identity columns: status and last_error always, whatsapp_jid when
// whatsappJID is not empty (keeping the stored one otherwise) and
// last_connected_at when connectedAt is set.
func (r *InstanceRepository) SetConnectionState(ctx context.Context, id uuid.UUID, status, whatsappJID, lastError string, connectedAt *time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances
		SET status = $2,
		    whatsapp_jid = COALESCE(NULLIF($3, ''), whatsapp_jid),
		    last_error = NULLIF($4, ''),
		    last_connected_at = COALESCE($5, last_connected_at),
		    updated_at = now()
		WHERE id = $1`,
		id, status, whatsappJID, lastError, connectedAt,
	)
	if err != nil {
		return fmt.Errorf("set instance connection state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set instance connection state: %w", storage.ErrNotFound)
	}
	return nil
}

// BackfillOwner claims every legacy instance with a NULL owner for owner and
// returns how many rows were claimed. Instances that already have an owner are
// never touched: ownership is immutable.
func (r *InstanceRepository) BackfillOwner(ctx context.Context, owner uuid.UUID) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances
		SET owner_user_id = $1, updated_at = now()
		WHERE owner_user_id IS NULL`,
		owner,
	)
	if err != nil {
		return 0, fmt.Errorf("backfill instance owners: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Delete removes the instance and its dependent rows, or storage.ErrNotFound.
func (r *InstanceRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM instances WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete instance: %w", storage.ErrNotFound)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstance(scanner rowScanner) (*model.Instance, error) {
	var instance model.Instance
	if err := scanInstanceRow(scanner, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

func scanInstanceRow(scanner rowScanner, instance *model.Instance) error {
	return scanner.Scan(
		&instance.ID, &instance.Name, &instance.ExternalRef, &instance.Status,
		&instance.WhatsAppJID, &instance.LastConnectedAt, &instance.LastError,
		&instance.OwnerUserID, &instance.WebhookURL, &instance.WebhookEnabled, &instance.WebhookEvents,
		&instance.CreatedAt, &instance.UpdatedAt,
	)
}

func mapInstanceError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "instances_external_ref_key" {
		return fmt.Errorf("%s: %w", op, storage.ErrExternalRefTaken)
	}
	return fmt.Errorf("%s: %w", op, err)
}
