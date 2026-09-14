package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/storage"
)

func createTestUser(t *testing.T, repo storage.UserRepository, email, role string, quota int) *model.User {
	t.Helper()

	user, err := repo.Create(context.Background(), model.User{
		ID:            uuid.New(),
		Email:         email,
		PasswordHash:  "hash-for-" + email,
		Role:          role,
		InstanceQuota: quota,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return user
}

func TestUserRepositoryCreate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewUserRepository(pool)
	start := time.Now()

	created, err := repo.Create(ctx, model.User{
		ID:            uuid.New(),
		Email:         "admin@example.com",
		PasswordHash:  "hashed-secret",
		Role:          "admin",
		InstanceQuota: 5,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.ID == uuid.Nil {
		t.Error("Create: ID is nil")
	}
	if created.Email != "admin@example.com" {
		t.Errorf("Create: Email = %q, want %q", created.Email, "admin@example.com")
	}
	if created.PasswordHash != "hashed-secret" {
		t.Errorf("Create: PasswordHash was not returned as stored")
	}
	if created.Role != "admin" {
		t.Errorf("Create: Role = %q, want %q", created.Role, "admin")
	}
	if created.InstanceQuota != 5 {
		t.Errorf("Create: InstanceQuota = %d, want 5", created.InstanceQuota)
	}
	requireTimeBetween(t, "Create: CreatedAt", created.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
	requireTimeBetween(t, "Create: UpdatedAt", created.UpdatedAt, start.Add(-time.Second), time.Now().Add(time.Second))

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count user: %v", err)
	}
	if count != 1 {
		t.Errorf("users with created id = %d, want 1", count)
	}
}

func TestUserRepositoryCreateDuplicateEmail(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewUserRepository(pool)

	createTestUser(t, repo, "dup@example.com", "admin", 0)

	_, err := repo.Create(ctx, model.User{
		ID:            uuid.New(),
		Email:         "dup@example.com",
		PasswordHash:  "other-hash",
		Role:          "user",
		InstanceQuota: 0,
	})
	if !errors.Is(err, storage.ErrEmailTaken) {
		t.Fatalf("Create duplicate email error = %v, want ErrEmailTaken", err)
	}

	_, err = repo.Create(ctx, model.User{
		ID:            uuid.New(),
		Email:         "DUP@EXAMPLE.COM",
		PasswordHash:  "other-hash",
		Role:          "user",
		InstanceQuota: 0,
	})
	if !errors.Is(err, storage.ErrEmailTaken) {
		t.Fatalf("Create duplicate email (case-insensitive) error = %v, want ErrEmailTaken", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("users count = %d, want 1", count)
	}
}

func TestUserRepositoryGetByID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	_ = pool
	repo := NewUserRepository(pool)

	created := createTestUser(t, repo, "getbyid@example.com", "user", 2)

	got, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID || got.Email != created.Email || got.Role != "user" || got.InstanceQuota != 2 {
		t.Errorf("GetByID returned %+v, want %+v", got, created)
	}
	if got.PasswordHash != "hash-for-getbyid@example.com" {
		t.Errorf("GetByID PasswordHash = %q, want the stored hash", got.PasswordHash)
	}

	_, err = repo.GetByID(ctx, uuid.New())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByID(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestUserRepositoryGetByEmail(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	_ = pool
	repo := NewUserRepository(pool)

	created := createTestUser(t, repo, "Mixed.Case@Example.com", "admin", 0)

	for _, variant := range []string{"Mixed.Case@Example.com", "mixed.case@example.com", "MIXED.CASE@EXAMPLE.COM"} {
		got, err := repo.GetByEmail(ctx, variant)
		if err != nil {
			t.Fatalf("GetByEmail(%q): %v", variant, err)
		}
		if got.ID != created.ID {
			t.Errorf("GetByEmail(%q) ID = %s, want %s", variant, got.ID, created.ID)
		}
	}

	_, err := repo.GetByEmail(ctx, "missing@example.com")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByEmail(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestUserRepositoryList(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	_ = pool
	repo := NewUserRepository(pool)

	empty, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List(empty) returned %d users, want 0", len(empty))
	}

	first := createTestUser(t, repo, "first@example.com", "admin", 0)
	second := createTestUser(t, repo, "second@example.com", "user", 1)
	third := createTestUser(t, repo, "third@example.com", "user", 2)

	users, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("List returned %d users, want 3", len(users))
	}
	wantOrder := []uuid.UUID{first.ID, second.ID, third.ID}
	for i, want := range wantOrder {
		if users[i].ID != want {
			t.Errorf("List order [%d] = %s, want %s (full: %v)", i, users[i].ID, want, wantOrder)
		}
	}
	for i := 1; i < len(users); i++ {
		if users[i].CreatedAt.Before(users[i-1].CreatedAt) {
			t.Errorf("List not ordered by created_at: [%d] %s before [%d] %s",
				i, users[i].CreatedAt, i-1, users[i-1].CreatedAt)
		}
	}
}

func TestUserRepositoryDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewUserRepository(pool)

	user := createTestUser(t, repo, "todelete@example.com", "user", 0)

	if err := repo.Delete(ctx, user.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, user.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByID after Delete error = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, user.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Delete twice error = %v, want ErrNotFound", err)
	}
}

func TestUserRepositoryDeleteOwnerWithInstances(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	users := NewUserRepository(pool)
	instances := NewInstanceRepository(pool)

	owner := createTestUser(t, users, "owner@example.com", "user", 5)
	instance := createTestInstance(t, instances, "owned", "owned-ref")
	setInstanceOwner(t, pool, instance.ID, owner.ID)

	err := users.Delete(ctx, owner.ID)
	if err == nil {
		t.Fatal("Delete owner with instances: got nil, want FK error")
	}
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrEmailTaken) {
		t.Fatalf("Delete owner with instances error = %v, want plain FK error", err)
	}

	if _, err := users.GetByID(ctx, owner.ID); err != nil {
		t.Errorf("owner missing after failed Delete: %v", err)
	}
}

func TestUserRepositoryCount(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	_ = pool
	repo := NewUserRepository(pool)

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count(empty): %v", err)
	}
	if count != 0 {
		t.Errorf("Count(empty) = %d, want 0", count)
	}

	createTestUser(t, repo, "one@example.com", "admin", 0)
	createTestUser(t, repo, "two@example.com", "user", 0)

	count, err = repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}
}
