package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage/postgres"
	"wzap/internal/storage/postgres/postgrestest"
)

// newSeedRepos returns a pool plus migrated user and instance repositories
// bound to an isolated Postgres schema, so seed tests never share state.
func newSeedRepos(t *testing.T) (*pgxpool.Pool, *postgres.UserRepository, *postgres.InstanceRepository) {
	t.Helper()

	pool := postgrestest.NewPool(t)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	return pool, postgres.NewUserRepository(pool), postgres.NewInstanceRepository(pool)
}

func seedTestConfig(email, password string) config.Config {
	return config.Config{
		AdminEmail:       email,
		AdminPassword:    password,
		DefaultUserQuota: 3,
	}
}

func seedTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func createSeedUser(t *testing.T, users *postgres.UserRepository, email string) *model.User {
	t.Helper()

	user, err := users.Create(context.Background(), model.User{
		ID:            uuid.New(),
		Email:         email,
		PasswordHash:  "hash-for-" + email,
		Role:          "user",
		InstanceQuota: 1,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return user
}

func createLegacyInstance(t *testing.T, instances *postgres.InstanceRepository, name string) *model.Instance {
	t.Helper()

	instance, err := instances.Create(context.Background(), model.Instance{
		ID:     uuid.New(),
		Name:   name,
		Status: "disconnected",
	})
	if err != nil {
		t.Fatalf("create instance %q: %v", name, err)
	}
	if instance.OwnerUserID != nil {
		t.Fatalf("create instance %q: OwnerUserID = %v, want nil (legacy row)", name, instance.OwnerUserID)
	}
	return instance
}

func setSeedOwner(t *testing.T, pool *pgxpool.Pool, instanceID, ownerID uuid.UUID) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE instances SET owner_user_id = $2 WHERE id = $1`, instanceID, ownerID); err != nil {
		t.Fatalf("set instance owner: %v", err)
	}
}

func TestSeedAdminEmptyWithEnvs(t *testing.T) {
	ctx := context.Background()
	_, users, instances := newSeedRepos(t)

	first := createLegacyInstance(t, instances, "legacy-one")
	second := createLegacyInstance(t, instances, "legacy-two")

	cfg := seedTestConfig("admin@example.com", "s3cret-password")
	if err := seedAdmin(ctx, cfg, users, instances, seedTestLogger()); err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}

	count, err := users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("users count = %d, want 1", count)
	}

	admin, err := users.GetByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatalf("GetByEmail(admin): %v", err)
	}
	if admin.Email != "admin@example.com" {
		t.Errorf("admin Email = %q, want verbatim %q", admin.Email, "admin@example.com")
	}
	if admin.Role != "admin" {
		t.Errorf("admin Role = %q, want admin", admin.Role)
	}
	if admin.InstanceQuota != cfg.DefaultUserQuota {
		t.Errorf("admin InstanceQuota = %d, want %d", admin.InstanceQuota, cfg.DefaultUserQuota)
	}
	if err := auth.CheckPassword(admin.PasswordHash, "s3cret-password"); err != nil {
		t.Errorf("CheckPassword(admin hash): %v, want the seed password to verify", err)
	}

	for _, id := range []uuid.UUID{first.ID, second.ID} {
		got, err := instances.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s) after seed: %v", id, err)
		}
		if got.OwnerUserID == nil || *got.OwnerUserID != admin.ID {
			t.Errorf("Get(%s) OwnerUserID = %v, want seeded admin %s", id, got.OwnerUserID, admin.ID)
		}
	}
}

func TestSeedAdminEmptyWithoutEnvs(t *testing.T) {
	ctx := context.Background()
	_, users, instances := newSeedRepos(t)

	legacy := createLegacyInstance(t, instances, "legacy")

	if err := seedAdmin(ctx, seedTestConfig("", ""), users, instances, seedTestLogger()); err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}

	count, err := users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Errorf("users count = %d, want 0 (no envs, nothing created)", count)
	}

	got, err := instances.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("Get(legacy) after seed: %v", err)
	}
	if got.OwnerUserID != nil {
		t.Errorf("Get(legacy) OwnerUserID = %v, want nil (no admin to claim it)", got.OwnerUserID)
	}
}

func TestSeedAdminNonEmptyWithEnvs(t *testing.T) {
	ctx := context.Background()
	pool, users, instances := newSeedRepos(t)

	existing := createSeedUser(t, users, "owner@example.com")
	legacy := createLegacyInstance(t, instances, "legacy")
	owned := createLegacyInstance(t, instances, "owned")
	setSeedOwner(t, pool, owned.ID, existing.ID)

	if err := seedAdmin(ctx, seedTestConfig("admin@example.com", "s3cret-password"), users, instances, seedTestLogger()); err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}

	count, err := users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("users count = %d, want 1 (seed is a no-op, never duplicates)", count)
	}

	// Owners are untouched: the NULL-owner row stays NULL and the owned row
	// keeps its owner. Ownership is immutable.
	got, err := instances.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("Get(legacy) after seed: %v", err)
	}
	if got.OwnerUserID != nil {
		t.Errorf("Get(legacy) OwnerUserID = %v, want nil (untouched)", got.OwnerUserID)
	}

	got, err = instances.Get(ctx, owned.ID)
	if err != nil {
		t.Fatalf("Get(owned) after seed: %v", err)
	}
	if got.OwnerUserID == nil || *got.OwnerUserID != existing.ID {
		t.Errorf("Get(owned) OwnerUserID = %v, want untouched %s", got.OwnerUserID, existing.ID)
	}
}

func TestSeedAdminNonEmptyWithoutEnvs(t *testing.T) {
	ctx := context.Background()
	_, users, instances := newSeedRepos(t)

	createSeedUser(t, users, "owner@example.com")
	legacy := createLegacyInstance(t, instances, "legacy")

	if err := seedAdmin(ctx, seedTestConfig("", ""), users, instances, seedTestLogger()); err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}

	count, err := users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("users count = %d, want 1", count)
	}

	got, err := instances.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("Get(legacy) after seed: %v", err)
	}
	if got.OwnerUserID != nil {
		t.Errorf("Get(legacy) OwnerUserID = %v, want nil (untouched)", got.OwnerUserID)
	}
}

func TestSeedAdminHalfConfigured(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
	}{
		{name: "email only", email: "admin@example.com", password: ""},
		{name: "password only", email: "", password: "s3cret-password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, users, instances := newSeedRepos(t)

			legacy := createLegacyInstance(t, instances, "legacy")

			// A half-configured seed warns, creates nothing and lets boot
			// proceed: the nil error is the assertion that boot continues.
			if err := seedAdmin(ctx, seedTestConfig(tt.email, tt.password), users, instances, seedTestLogger()); err != nil {
				t.Fatalf("seedAdmin(half-configured) = %v, want nil (boot proceeds)", err)
			}

			count, err := users.Count(ctx)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if count != 0 {
				t.Errorf("users count = %d, want 0 (half-configured seed creates nothing)", count)
			}

			got, err := instances.Get(ctx, legacy.ID)
			if err != nil {
				t.Fatalf("Get(legacy) after seed: %v", err)
			}
			if got.OwnerUserID != nil {
				t.Errorf("Get(legacy) OwnerUserID = %v, want nil (untouched)", got.OwnerUserID)
			}
		})
	}
}
