package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestInstanceRepositoryBackfillOwner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	users := NewUserRepository(pool)
	repo := NewInstanceRepository(pool)

	admin := createTestUser(t, users, "admin@example.com", "admin", 0)
	other := createTestUser(t, users, "other@example.com", "user", 0)

	first := createTestInstance(t, repo, "legacy-one", "legacy-one")
	second := createTestInstance(t, repo, "legacy-two", "legacy-two")
	owned := createTestInstance(t, repo, "owned", "owned-ref")
	setInstanceOwner(t, pool, owned.ID, other.ID)

	claimed, err := repo.BackfillOwner(ctx, admin.ID)
	if err != nil {
		t.Fatalf("BackfillOwner: %v", err)
	}
	if claimed != 2 {
		t.Errorf("BackfillOwner claimed = %d, want 2", claimed)
	}

	for _, id := range []uuid.UUID{first.ID, second.ID} {
		got, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s) after backfill: %v", id, err)
		}
		if got.OwnerUserID == nil || *got.OwnerUserID != admin.ID {
			t.Errorf("Get(%s) OwnerUserID = %v, want %s", id, got.OwnerUserID, admin.ID)
		}
	}

	got, err := repo.Get(ctx, owned.ID)
	if err != nil {
		t.Fatalf("Get(owned) after backfill: %v", err)
	}
	if got.OwnerUserID == nil || *got.OwnerUserID != other.ID {
		t.Errorf("Get(owned) OwnerUserID = %v, want untouched %s", got.OwnerUserID, other.ID)
	}

	claimed, err = repo.BackfillOwner(ctx, admin.ID)
	if err != nil {
		t.Fatalf("BackfillOwner again: %v", err)
	}
	if claimed != 0 {
		t.Errorf("BackfillOwner again claimed = %d, want 0", claimed)
	}
}

func TestInstanceRepositoryBackfillOwnerUnknownOwner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	createTestInstance(t, repo, "legacy", "legacy-ref")

	if _, err := repo.BackfillOwner(ctx, uuid.New()); err == nil {
		t.Fatal("BackfillOwner(unknown owner) = nil, want FK error")
	}

	got, err := repo.Get(ctx, mustInstanceID(t, ctx, repo))
	if err != nil {
		t.Fatalf("Get after failed backfill: %v", err)
	}
	if got.OwnerUserID != nil {
		t.Errorf("OwnerUserID = %v after failed backfill, want nil", got.OwnerUserID)
	}
}

func mustInstanceID(t *testing.T, ctx context.Context, repo *InstanceRepository) uuid.UUID {
	t.Helper()

	instances, _, err := repo.List(ctx, 1, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("List returned %d instances, want 1", len(instances))
	}
	return instances[0].ID
}
