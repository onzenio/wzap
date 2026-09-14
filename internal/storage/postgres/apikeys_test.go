package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/storage"
)

func setInstanceOwner(t *testing.T, pool *pgxpool.Pool, instanceID, ownerID uuid.UUID) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE instances SET owner_user_id = $2 WHERE id = $1`, instanceID, ownerID); err != nil {
		t.Fatalf("set instance owner: %v", err)
	}
}

func TestAPIKeySetAndInstanceByHash(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	keys := NewAPIKeyRepository(pool)

	instance := createTestInstance(t, instances, "keyed", "keyed-ref")

	if err := keys.SetHash(ctx, instance.ID, "hash-aaa"); err != nil {
		t.Fatalf("SetHash: %v", err)
	}

	got, err := keys.InstanceByHash(ctx, "hash-aaa")
	if err != nil {
		t.Fatalf("InstanceByHash: %v", err)
	}
	if got != instance.ID {
		t.Errorf("InstanceByHash = %s, want %s", got, instance.ID)
	}

	if _, err := keys.InstanceByHash(ctx, "unknown-hash"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("InstanceByHash(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestAPIKeySetHashReplaces(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	keys := NewAPIKeyRepository(pool)

	instance := createTestInstance(t, instances, "rotated", "rotated-ref")

	if err := keys.SetHash(ctx, instance.ID, "hash-old"); err != nil {
		t.Fatalf("SetHash(old): %v", err)
	}
	if err := keys.SetHash(ctx, instance.ID, "hash-new"); err != nil {
		t.Fatalf("SetHash(new): %v", err)
	}

	if _, err := keys.InstanceByHash(ctx, "hash-old"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("InstanceByHash(old) error = %v, want ErrNotFound", err)
	}
	got, err := keys.InstanceByHash(ctx, "hash-new")
	if err != nil {
		t.Fatalf("InstanceByHash(new): %v", err)
	}
	if got != instance.ID {
		t.Errorf("InstanceByHash(new) = %s, want %s", got, instance.ID)
	}
}

func TestAPIKeySetHashNotFound(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	keys := NewAPIKeyRepository(pool)

	if err := keys.SetHash(ctx, uuid.New(), "hash-ghost"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("SetHash(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestAPIKeyClearHash(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	keys := NewAPIKeyRepository(pool)

	instance := createTestInstance(t, instances, "revoked", "revoked-ref")
	if err := keys.SetHash(ctx, instance.ID, "hash-live"); err != nil {
		t.Fatalf("SetHash: %v", err)
	}

	if err := keys.ClearHash(ctx, instance.ID); err != nil {
		t.Fatalf("ClearHash: %v", err)
	}
	if _, err := keys.InstanceByHash(ctx, "hash-live"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("InstanceByHash after ClearHash error = %v, want ErrNotFound", err)
	}

	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT api_key_hash IS NULL FROM instances WHERE id = $1`, instance.ID).Scan(&isNull); err != nil {
		t.Fatalf("check api_key_hash NULL: %v", err)
	}
	if !isNull {
		t.Error("api_key_hash is not NULL after ClearHash")
	}

	if err := keys.ClearHash(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("ClearHash(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestAPIKeyCounts(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	users := NewUserRepository(pool)
	instances := NewInstanceRepository(pool)
	keys := NewAPIKeyRepository(pool)

	total, err := keys.CountAll(ctx)
	if err != nil {
		t.Fatalf("CountAll(empty): %v", err)
	}
	if total != 0 {
		t.Errorf("CountAll(empty) = %d, want 0", total)
	}

	ownerA := createTestUser(t, users, "counter-a@example.com", "user", 5)
	ownerB := createTestUser(t, users, "counter-b@example.com", "user", 5)

	a1 := createTestInstance(t, instances, "a1", "a1-ref")
	a2 := createTestInstance(t, instances, "a2", "a2-ref")
	b1 := createTestInstance(t, instances, "b1", "b1-ref")
	setInstanceOwner(t, pool, a1.ID, ownerA.ID)
	setInstanceOwner(t, pool, a2.ID, ownerA.ID)
	setInstanceOwner(t, pool, b1.ID, ownerB.ID)

	legacy := createTestInstance(t, instances, "legacy", "legacy-ref")
	_ = legacy

	total, err = keys.CountAll(ctx)
	if err != nil {
		t.Fatalf("CountAll: %v", err)
	}
	if total != 4 {
		t.Errorf("CountAll = %d, want 4 (every instance counts, any state)", total)
	}

	countA, err := keys.CountByOwner(ctx, ownerA.ID)
	if err != nil {
		t.Fatalf("CountByOwner(A): %v", err)
	}
	if countA != 2 {
		t.Errorf("CountByOwner(A) = %d, want 2", countA)
	}

	countB, err := keys.CountByOwner(ctx, ownerB.ID)
	if err != nil {
		t.Fatalf("CountByOwner(B): %v", err)
	}
	if countB != 1 {
		t.Errorf("CountByOwner(B) = %d, want 1", countB)
	}

	none, err := keys.CountByOwner(ctx, uuid.New())
	if err != nil {
		t.Fatalf("CountByOwner(unknown): %v", err)
	}
	if none != 0 {
		t.Errorf("CountByOwner(unknown) = %d, want 0", none)
	}
}

func TestInstanceRepositoryReadsProductColumns(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	users := NewUserRepository(pool)
	instances := NewInstanceRepository(pool)

	created := createTestInstance(t, instances, "plain", "plain-ref")
	got, err := instances.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OwnerUserID != nil {
		t.Errorf("OwnerUserID = %v, want nil for legacy instance", got.OwnerUserID)
	}
	if got.WebhookURL != nil {
		t.Errorf("WebhookURL = %v, want nil for legacy instance", got.WebhookURL)
	}
	if got.WebhookEnabled {
		t.Error("WebhookEnabled = true, want false by default")
	}
	wantDefaultEvents := []string{"message", "receipt", "connection", "message.status"}
	if len(got.WebhookEvents) != len(wantDefaultEvents) {
		t.Fatalf("WebhookEvents = %v, want default %v", got.WebhookEvents, wantDefaultEvents)
	}
	for i, want := range wantDefaultEvents {
		if got.WebhookEvents[i] != want {
			t.Errorf("WebhookEvents[%d] = %q, want %q", i, got.WebhookEvents[i], want)
		}
	}

	owner := createTestUser(t, users, "webhook-owner@example.com", "user", 5)
	owned := createTestInstance(t, instances, "owned", "owned-ref")
	if _, err := pool.Exec(ctx,
		`UPDATE instances SET owner_user_id = $2, webhook_url = $3, webhook_enabled = true,
		 webhook_events = $4 WHERE id = $1`,
		owned.ID, owner.ID, "https://example.com/hook", []string{"message"}); err != nil {
		t.Fatalf("seed product columns: %v", err)
	}

	got, err = instances.Get(ctx, owned.ID)
	if err != nil {
		t.Fatalf("Get(owned): %v", err)
	}
	if got.OwnerUserID == nil || *got.OwnerUserID != owner.ID {
		t.Errorf("OwnerUserID = %v, want %s", got.OwnerUserID, owner.ID)
	}
	if got.WebhookURL == nil || *got.WebhookURL != "https://example.com/hook" {
		t.Errorf("WebhookURL = %v, want https://example.com/hook", got.WebhookURL)
	}
	if !got.WebhookEnabled {
		t.Error("WebhookEnabled = false, want true")
	}
	if len(got.WebhookEvents) != 1 || got.WebhookEvents[0] != "message" {
		t.Errorf("WebhookEvents = %v, want [message]", got.WebhookEvents)
	}
}
