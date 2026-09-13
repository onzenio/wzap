package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/storage"
)

const testIdempotencyTTL = 24 * time.Hour

func backdateIdempotencyExpiry(t *testing.T, pool *pgxpool.Pool, instanceID uuid.UUID, key string, at time.Time) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET expires_at = $3 WHERE instance_id = $1 AND idempotency_key = $2`,
		instanceID, key, at); err != nil {
		t.Fatalf("backdate idempotency expiry: %v", err)
	}
}

func TestIdempotencyRepositoryAcquireOwnsNewKey(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	start := time.Now()
	expiresAt := start.Add(testIdempotencyTTL)

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-1", "fingerprint-a", expiresAt)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !acquired {
		t.Error("Acquire(new key) acquired = false, want true")
	}
	if record == nil {
		t.Fatal("Acquire(new key) record = nil")
	}
	if record.InstanceID != instance.ID {
		t.Errorf("Acquire record InstanceID = %s, want %s", record.InstanceID, instance.ID)
	}
	if record.Key != "key-1" || record.Fingerprint != "fingerprint-a" {
		t.Errorf("Acquire record key/fingerprint = %q/%q, want key-1/fingerprint-a", record.Key, record.Fingerprint)
	}
	if record.Status != "in_progress" {
		t.Errorf("Acquire record Status = %q, want in_progress", record.Status)
	}
	if record.ResponseStatus != 0 {
		t.Errorf("Acquire record ResponseStatus = %d, want 0", record.ResponseStatus)
	}
	if len(record.ResponseBody) != 0 {
		t.Errorf("Acquire record ResponseBody = %q, want empty", record.ResponseBody)
	}
	requireTimeBetween(t, "Acquire: CreatedAt", record.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
	requireTimeNear(t, "Acquire: ExpiresAt", record.ExpiresAt, expiresAt)

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`,
		instance.ID, "key-1").Scan(&count); err != nil {
		t.Fatalf("count idempotency key: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotency_keys rows for key-1 = %d, want 1", count)
	}
}

func TestIdempotencyRepositoryAcquireUnknownInstance(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewIdempotencyRepository(pool)

	_, acquired, err := repo.Acquire(ctx, uuid.New(), "key-1", "fingerprint-a", time.Now().Add(testIdempotencyTTL))
	if acquired {
		t.Error("Acquire(unknown instance) acquired = true, want false")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Acquire(unknown instance) error = %v, want ErrNotFound", err)
	}
}

func TestIdempotencyRepositoryAcquireRace(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")

	const callers = 2
	start := make(chan struct{})
	acquiredResults := make([]bool, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, acquiredResults[i], errs[i] = repo.Acquire(
				ctx, instance.ID, "race-key", "fingerprint-a", time.Now().Add(testIdempotencyTTL))
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i := 0; i < callers; i++ {
		switch {
		case errs[i] == nil && acquiredResults[i]:
			won++
		case errors.Is(errs[i], storage.ErrInProgress):
		default:
			t.Fatalf("goroutine %d: acquired=%v err=%v, want one winner and one ErrInProgress",
				i, acquiredResults[i], errs[i])
		}
	}
	if won != 1 {
		t.Errorf("Acquire race winners = %d, want exactly 1", won)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`,
		instance.ID, "race-key").Scan(&count); err != nil {
		t.Fatalf("count race key: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotency_keys rows for race-key = %d, want 1", count)
	}
}

func TestIdempotencyRepositoryAcquireInProgress(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-2", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-2", "fingerprint-a", expiresAt)
	if acquired {
		t.Error("second Acquire acquired = true, want false")
	}
	if record != nil {
		t.Errorf("second Acquire record = %+v, want nil", record)
	}
	if !errors.Is(err, storage.ErrInProgress) {
		t.Errorf("second Acquire error = %v, want ErrInProgress", err)
	}
}

func TestIdempotencyRepositoryAcquireReplay(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)
	body := []byte(`{"data":{"message_id":"0f7c1c74-3f0a-4e0f-9a5f-3b9b2f0f7f11"}}`)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-3", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}
	if err := repo.Complete(ctx, instance.ID, "key-3", 202, body); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	replayed, acquired, err := repo.Acquire(ctx, instance.ID, "key-3", "fingerprint-a", expiresAt)
	if err != nil {
		t.Fatalf("replay Acquire: %v", err)
	}
	if acquired {
		t.Error("replay Acquire acquired = true, want false")
	}
	if replayed == nil {
		t.Fatal("replay Acquire record = nil")
	}
	if replayed.Status != "completed" {
		t.Errorf("replay record Status = %q, want completed", replayed.Status)
	}
	if replayed.ResponseStatus != 202 {
		t.Errorf("replay record ResponseStatus = %d, want 202", replayed.ResponseStatus)
	}
	requireJSONEqual(t, "replay record ResponseBody", replayed.ResponseBody, body)
}

func TestIdempotencyRepositoryAcquireFingerprintMismatch(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-4", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-4", "fingerprint-b", expiresAt)
	if acquired {
		t.Error("mismatch Acquire acquired = true, want false")
	}
	if record != nil {
		t.Errorf("mismatch Acquire record = %+v, want nil", record)
	}
	if !errors.Is(err, storage.ErrFingerprintMismatch) {
		t.Errorf("mismatch Acquire error = %v, want ErrFingerprintMismatch", err)
	}

	if err := repo.Complete(ctx, instance.ID, "key-4", 202, []byte(`{"data":{"message_id":"abc"}}`)); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	_, acquired, err = repo.Acquire(ctx, instance.ID, "key-4", "fingerprint-b", expiresAt)
	if acquired {
		t.Error("mismatch after complete acquired = true, want false")
	}
	if !errors.Is(err, storage.ErrFingerprintMismatch) {
		t.Errorf("mismatch after complete error = %v, want ErrFingerprintMismatch", err)
	}

	replayed, acquired, err := repo.Acquire(ctx, instance.ID, "key-4", "fingerprint-a", expiresAt)
	if err != nil {
		t.Fatalf("original fingerprint Acquire: %v", err)
	}
	if acquired {
		t.Error("original fingerprint Acquire acquired = true, want false (replay)")
	}
	if replayed.Status != "completed" {
		t.Errorf("original record Status = %q, want completed", replayed.Status)
	}
}

func TestIdempotencyRepositoryAcquireExpiredKeyIsReusable(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-5", "fingerprint-a", time.Now().Add(-time.Hour)); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-5", "fingerprint-a", time.Now().Add(testIdempotencyTTL))
	if err != nil {
		t.Fatalf("reacquire expired key: %v", err)
	}
	if !acquired {
		t.Error("reacquire expired key acquired = false, want true")
	}
	if record == nil || record.Status != "in_progress" {
		t.Fatalf("reacquire expired key record = %+v, want in_progress", record)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`,
		instance.ID, "key-5").Scan(&count); err != nil {
		t.Fatalf("count key-5: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotency_keys rows for key-5 = %d, want 1", count)
	}
}

func TestIdempotencyRepositoryAcquireExpiredCompletedKeyIsReusable(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-6", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}
	if err := repo.Complete(ctx, instance.ID, "key-6", 202, []byte(`{"data":{"message_id":"abc"}}`)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	backdateIdempotencyExpiry(t, pool, instance.ID, "key-6", time.Now().Add(-time.Hour))

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-6", "fingerprint-b", expiresAt)
	if err != nil {
		t.Fatalf("reacquire expired completed key: %v", err)
	}
	if !acquired {
		t.Error("reacquire expired completed key acquired = false, want true")
	}
	if record == nil || record.Status != "in_progress" {
		t.Fatalf("reacquire expired completed key record = %+v, want in_progress", record)
	}
	if record.ResponseStatus != 0 || len(record.ResponseBody) != 0 {
		t.Errorf("reacquired record response = %d/%q, want 0/empty", record.ResponseStatus, record.ResponseBody)
	}
}

func TestIdempotencyRepositoryCompleteUnknownKey(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")

	err := repo.Complete(ctx, instance.ID, "missing", 200, []byte(`{"data":{}}`))
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Complete(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestIdempotencyRepositoryRelease(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-7", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("first Acquire: acquired=%v err=%v, want true and nil", acquired, err)
	}
	if err := repo.Release(ctx, instance.ID, "key-7"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	record, acquired, err := repo.Acquire(ctx, instance.ID, "key-7", "fingerprint-b", expiresAt)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if !acquired {
		t.Error("Acquire after Release acquired = false, want true")
	}
	if record == nil || record.Status != "in_progress" {
		t.Fatalf("Acquire after Release record = %+v, want in_progress", record)
	}

	if err := repo.Release(ctx, instance.ID, "key-7"); err != nil {
		t.Errorf("Release(acquired key) error = %v, want nil", err)
	}
	if err := repo.Release(ctx, instance.ID, "key-7"); err != nil {
		t.Errorf("Release(absent key) error = %v, want nil (idempotent)", err)
	}
}

func TestIdempotencyRepositoryDeleteExpired(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	repo := NewIdempotencyRepository(pool)
	instance := createTestInstance(t, instances, "idempotency", "")
	expiresAt := time.Now().Add(testIdempotencyTTL)

	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-live", "fingerprint-a", expiresAt); err != nil || !acquired {
		t.Fatalf("Acquire live: acquired=%v err=%v, want true and nil", acquired, err)
	}
	if _, acquired, err := repo.Acquire(ctx, instance.ID, "key-stale", "fingerprint-a", time.Now().Add(-time.Hour)); err != nil || !acquired {
		t.Fatalf("Acquire stale: acquired=%v err=%v, want true and nil", acquired, err)
	}

	removed, err := repo.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeleteExpired = %d, want 1", removed)
	}

	_, _, err = repo.Acquire(ctx, instance.ID, "key-live", "fingerprint-a", expiresAt)
	if !errors.Is(err, storage.ErrInProgress) {
		t.Errorf("Acquire(live after DeleteExpired) error = %v, want ErrInProgress", err)
	}

	_, acquired, err := repo.Acquire(ctx, instance.ID, "key-stale", "fingerprint-a", expiresAt)
	if err != nil {
		t.Fatalf("Acquire(stale after DeleteExpired): %v", err)
	}
	if !acquired {
		t.Error("Acquire(stale after DeleteExpired) acquired = false, want true")
	}
}
