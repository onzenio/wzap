package postgres

import (
	"context"
	"testing"
	"time"
)

func TestJIDCacheRepositoryPutAndGet(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewJIDCacheRepository(pool)
	start := time.Now()
	expiresAt := start.Add(24 * time.Hour)

	if err := repo.Put(ctx, "5511999999999", "5511999999999@s.whatsapp.net", expiresAt); err != nil {
		t.Fatalf("Put: %v", err)
	}

	jid, ok, err := repo.Get(ctx, "5511999999999")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Error("Get(known phone) ok = false, want true")
	}
	if jid != "5511999999999@s.whatsapp.net" {
		t.Errorf("Get(known phone) jid = %q, want 5511999999999@s.whatsapp.net", jid)
	}

	jid, ok, err = repo.Get(ctx, "5511888888888")
	if err != nil {
		t.Fatalf("Get(unknown phone): %v", err)
	}
	if ok {
		t.Error("Get(unknown phone) ok = true, want false")
	}
	if jid != "" {
		t.Errorf("Get(unknown phone) jid = %q, want empty", jid)
	}

	var createdAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT created_at FROM contacts WHERE phone = $1`, "5511999999999").Scan(&createdAt); err != nil {
		t.Fatalf("select contacts created_at: %v", err)
	}
	requireTimeBetween(t, "Put: CreatedAt", createdAt, start.Add(-time.Second), time.Now().Add(time.Second))
}

func TestJIDCacheRepositoryPutOverwrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewJIDCacheRepository(pool)
	expiresAt := time.Now().Add(24 * time.Hour)

	if err := repo.Put(ctx, "5511999999999", "5511999999999@s.whatsapp.net", expiresAt); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := repo.Put(ctx, "5511999999999", "5511999999999@lid", expiresAt.Add(time.Hour)); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	jid, ok, err := repo.Get(ctx, "5511999999999")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || jid != "5511999999999@lid" {
		t.Errorf("Get after overwrite = %q/%v, want 5511999999999@lid/true", jid, ok)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM contacts WHERE phone = $1`, "5511999999999").Scan(&count); err != nil {
		t.Fatalf("count contacts rows: %v", err)
	}
	if count != 1 {
		t.Errorf("contacts rows = %d, want 1", count)
	}
}

func TestJIDCacheRepositoryGetHidesExpired(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewJIDCacheRepository(pool)

	if err := repo.Put(ctx, "5511999999999", "5511999999999@s.whatsapp.net", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	jid, ok, err := repo.Get(ctx, "5511999999999")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get(expired phone) ok = true, want false")
	}
	if jid != "" {
		t.Errorf("Get(expired phone) jid = %q, want empty", jid)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM contacts WHERE phone = $1`, "5511999999999").Scan(&count); err != nil {
		t.Fatalf("count contacts rows: %v", err)
	}
	if count != 1 {
		t.Errorf("contacts rows with expired entry = %d, want 1 (Get must not delete)", count)
	}
}

func TestJIDCacheRepositoryDeleteExpired(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewJIDCacheRepository(pool)

	if err := repo.Put(ctx, "5551999999999", "5551999999999@s.whatsapp.net", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("Put live: %v", err)
	}
	if err := repo.Put(ctx, "5551888888888", "5551888888888@s.whatsapp.net", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Put expired: %v", err)
	}

	removed, err := repo.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeleteExpired = %d, want 1", removed)
	}

	jid, ok, err := repo.Get(ctx, "5551999999999")
	if err != nil {
		t.Fatalf("Get(live): %v", err)
	}
	if !ok || jid != "5551999999999@s.whatsapp.net" {
		t.Errorf("Get(live) = %q/%v, want 5551999999999@s.whatsapp.net/true", jid, ok)
	}

	jid, ok, err = repo.Get(ctx, "5551888888888")
	if err != nil {
		t.Fatalf("Get(expired): %v", err)
	}
	if ok || jid != "" {
		t.Errorf("Get(expired after DeleteExpired) = %q/%v, want empty/false", jid, ok)
	}
}
