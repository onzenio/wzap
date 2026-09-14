package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
)

const userColumns = `id, COALESCE(email, '') AS email, COALESCE(password_hash, '') AS password_hash, ` +
	`COALESCE(role, '') AS role, instance_quota, created_at, updated_at`

// UserRepository is the pgx-backed storage.UserRepository.
type UserRepository struct {
	pool *pgxpool.Pool
}

var _ storage.UserRepository = (*UserRepository)(nil)

// NewUserRepository returns a user repository backed by pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Create persists a new user and returns it with database timestamps. It
// returns storage.ErrEmailTaken when the email is already in use,
// case-insensitively.
func (r *UserRepository) Create(ctx context.Context, user model.User) (*model.User, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, password_hash, role, instance_quota)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+userColumns,
		user.ID, user.Email, user.PasswordHash, user.Role, user.InstanceQuota,
	)

	created, err := scanUser(row)
	if err != nil {
		return nil, mapUserError("create user", err)
	}
	return created, nil
}

// GetByID returns the user with the given id or storage.ErrNotFound.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	user, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err != nil {
		return nil, mapUserError("get user", err)
	}
	return user, nil
}

// GetByEmail returns the user with the given email, matching
// case-insensitively, or storage.ErrNotFound.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	user, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email))
	if err != nil {
		return nil, mapUserError("get user by email", err)
	}
	return user, nil
}

// List returns every user ordered by created_at.
func (r *UserRepository) List(ctx context.Context) ([]model.User, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := []model.User{}
	for rows.Next() {
		var user model.User
		if err := scanUserRow(rows, &user); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// Delete removes the user or returns storage.ErrNotFound. Deleting a user that
// still owns instances surfaces the foreign key violation as a plain error.
func (r *UserRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete user: %w", storage.ErrNotFound)
	}
	return nil
}

// Count returns how many users exist.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// UpdateQuota stores a new per-user instance quota (0 means unlimited). It
// returns storage.ErrNotFound when the user does not exist.
func (r *UserRepository) UpdateQuota(ctx context.Context, id uuid.UUID, quota int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET instance_quota = $2, updated_at = now() WHERE id = $1`, id, quota)
	if err != nil {
		return fmt.Errorf("update user quota: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update user quota: %w", storage.ErrNotFound)
	}
	return nil
}

func scanUser(scanner rowScanner) (*model.User, error) {
	var user model.User
	if err := scanUserRow(scanner, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func scanUserRow(scanner rowScanner, user *model.User) error {
	return scanner.Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role,
		&user.InstanceQuota, &user.CreatedAt, &user.UpdatedAt,
	)
}

func mapUserError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_lower_idx" {
		return fmt.Errorf("%s: %w", op, storage.ErrEmailTaken)
	}
	return fmt.Errorf("%s: %w", op, err)
}
