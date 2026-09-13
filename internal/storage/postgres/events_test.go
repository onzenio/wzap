package postgres

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
)

func enqueueTestEvent(t *testing.T, repo storage.EventOutboxRepository, id uuid.UUID, subject string) {
	t.Helper()

	if err := repo.Enqueue(context.Background(), id, subject, []byte(`{"event_id":"`+id.String()+`"}`)); err != nil {
		t.Fatalf("Enqueue %s: %v", id, err)
	}
}

func backdateOutboxPublishedAt(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, at time.Time) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE event_outbox SET published_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("backdate outbox published_at: %v", err)
	}
}

func TestEventOutboxRepositoryEnqueueAndClaim(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)
	start := time.Now()

	firstID := uuid.New()
	firstEnvelope := []byte(`{"event_id":"` + firstID.String() + `","event_version":1}`)
	if err := repo.Enqueue(ctx, firstID, "wzap.instances.a.connection", firstEnvelope); err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	secondID := uuid.New()
	enqueueTestEvent(t, repo, secondID, "wzap.instances.a.message")

	events, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ClaimPending returned %d events, want 2", len(events))
	}

	byID := make(map[uuid.UUID]model.OutboxEvent, len(events))
	for _, event := range events {
		if event.Attempts != 0 {
			t.Errorf("event %s Attempts = %d, want 0", event.ID, event.Attempts)
		}
		if event.LastError != "" {
			t.Errorf("event %s LastError = %q, want empty", event.ID, event.LastError)
		}
		if event.PublishedAt != nil {
			t.Errorf("event %s PublishedAt = %v, want nil", event.ID, event.PublishedAt)
		}
		requireTimeBetween(t, "Claim: CreatedAt", event.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
		byID[event.ID] = event
	}

	first, ok := byID[firstID]
	if !ok {
		t.Fatalf("first event %s missing from claim: %+v", firstID, events)
	}
	if first.Subject != "wzap.instances.a.connection" {
		t.Errorf("first event Subject = %q, want wzap.instances.a.connection", first.Subject)
	}
	requireJSONEqual(t, "Claim: Envelope", first.Envelope, firstEnvelope)

	second, ok := byID[secondID]
	if !ok {
		t.Fatalf("second event %s missing from claim: %+v", secondID, events)
	}
	if second.Subject != "wzap.instances.a.message" {
		t.Errorf("second event Subject = %q, want wzap.instances.a.message", second.Subject)
	}
}

func TestEventOutboxRepositoryClaimPendingLimit(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)

	for i := 0; i < 3; i++ {
		enqueueTestEvent(t, repo, uuid.New(), "wzap.instances.a.message")
	}

	zero, err := repo.ClaimPending(ctx, 0)
	if err != nil {
		t.Fatalf("ClaimPending(0): %v", err)
	}
	if len(zero) != 0 {
		t.Errorf("ClaimPending(0) returned %d events, want 0", len(zero))
	}

	first, err := repo.ClaimPending(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimPending(2): %v", err)
	}
	if len(first) != 2 {
		t.Errorf("ClaimPending(2) returned %d events, want 2", len(first))
	}

	all, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending(10): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ClaimPending(10) returned %d events, want 3", len(all))
	}

	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		t.Fatalf("count pending events: %v", err)
	}
	if pending != 3 {
		t.Errorf("pending events after claims = %d, want 3 (ClaimPending must not publish)", pending)
	}
}

func TestEventOutboxRepositoryClaimPendingSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)

	lockedID := uuid.New()
	freeID := uuid.New()
	enqueueTestEvent(t, repo, lockedID, "wzap.instances.a.message")
	enqueueTestEvent(t, repo, freeID, "wzap.instances.a.message")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT id FROM event_outbox WHERE id = $1 FOR UPDATE`, lockedID); err != nil {
		t.Fatalf("lock row: %v", err)
	}

	claimed, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending with locked row: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != freeID {
		t.Fatalf("ClaimPending = %+v, want only %s", claimed, freeID)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock transaction: %v", err)
	}

	claimed, err = repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after unlock: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("ClaimPending after unlock returned %d events, want 2", len(claimed))
	}
	seen := map[uuid.UUID]bool{}
	for _, event := range claimed {
		seen[event.ID] = true
	}
	if !seen[lockedID] || !seen[freeID] {
		t.Errorf("ClaimPending after unlock IDs = %v, want both %s and %s", seen, lockedID, freeID)
	}
}

func TestEventOutboxRepositoryClaimPendingConcurrentWithEnqueue(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)

	const total = 20
	ids := make([]uuid.UUID, total)
	for i := range ids {
		ids[i] = uuid.New()
	}

	enqueueDone := make(chan struct{})
	go func() {
		defer close(enqueueDone)
		for i, id := range ids {
			if err := repo.Enqueue(ctx, id, "wzap.instances.test", []byte(`{"event_id":"`+id.String()+`"}`)); err != nil {
				t.Errorf("Enqueue(%d): %v", i, err)
				return
			}
		}
	}()

	for {
		if _, err := repo.ClaimPending(ctx, total); err != nil {
			t.Fatalf("ClaimPending during inserts: %v", err)
		}

		select {
		case <-enqueueDone:
			events, err := repo.ClaimPending(ctx, total)
			if err != nil {
				t.Fatalf("final ClaimPending: %v", err)
			}
			if len(events) != total {
				t.Fatalf("final ClaimPending returned %d events, want %d", len(events), total)
			}
			seen := make(map[uuid.UUID]bool, total)
			for _, event := range events {
				if seen[event.ID] {
					t.Errorf("event %s claimed twice in the final batch", event.ID)
				}
				seen[event.ID] = true
			}
			for _, id := range ids {
				if !seen[id] {
					t.Errorf("event %s missing from the final batch", id)
				}
			}
			return
		default:
			runtime.Gosched()
		}
	}
}

func TestEventOutboxRepositoryMarkPublished(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)

	publishedID := uuid.New()
	pendingID := uuid.New()
	enqueueTestEvent(t, repo, publishedID, "wzap.instances.a.message")
	enqueueTestEvent(t, repo, pendingID, "wzap.instances.a.message")

	start := time.Now()
	if err := repo.MarkPublished(ctx, publishedID); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	claimed, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after MarkPublished: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != pendingID {
		t.Fatalf("ClaimPending after MarkPublished = %+v, want only %s", claimed, pendingID)
	}

	var publishedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT published_at FROM event_outbox WHERE id = $1`, publishedID).Scan(&publishedAt); err != nil {
		t.Fatalf("select published_at: %v", err)
	}
	requireTimePtrNear(t, "MarkPublished: PublishedAt", publishedAt, start)

	if err := repo.MarkPublished(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MarkPublished(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestEventOutboxRepositoryMarkAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)
	id := uuid.New()
	enqueueTestEvent(t, repo, id, "wzap.instances.a.message")

	if err := repo.MarkAttempt(ctx, id, "broker unavailable"); err != nil {
		t.Fatalf("MarkAttempt: %v", err)
	}

	claimed, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after MarkAttempt: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("ClaimPending after MarkAttempt returned %d events, want 1", len(claimed))
	}
	if claimed[0].Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", claimed[0].Attempts)
	}
	if claimed[0].LastError != "broker unavailable" {
		t.Errorf("LastError = %q, want broker unavailable", claimed[0].LastError)
	}
	if claimed[0].PublishedAt != nil {
		t.Errorf("PublishedAt = %v, want nil (failed publish stays pending)", claimed[0].PublishedAt)
	}

	if err := repo.MarkAttempt(ctx, id, "broker still down"); err != nil {
		t.Fatalf("second MarkAttempt: %v", err)
	}
	claimed, err = repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after second MarkAttempt: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 2 || claimed[0].LastError != "broker still down" {
		t.Errorf("after second MarkAttempt = %+v, want attempts 2 and last error broker still down", claimed)
	}

	if err := repo.MarkAttempt(ctx, uuid.New(), "boom"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MarkAttempt(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestEventOutboxRepositoryDeletePublishedBefore(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewEventOutboxRepository(pool)

	oldID := uuid.New()
	recentID := uuid.New()
	pendingID := uuid.New()
	enqueueTestEvent(t, repo, oldID, "wzap.instances.a.message")
	enqueueTestEvent(t, repo, recentID, "wzap.instances.a.message")
	enqueueTestEvent(t, repo, pendingID, "wzap.instances.a.message")

	if err := repo.MarkPublished(ctx, oldID); err != nil {
		t.Fatalf("MarkPublished(old): %v", err)
	}
	if err := repo.MarkPublished(ctx, recentID); err != nil {
		t.Fatalf("MarkPublished(recent): %v", err)
	}
	backdateOutboxPublishedAt(t, pool, oldID, time.Now().Add(-2*time.Hour))

	if _, err := pool.Exec(ctx,
		`UPDATE event_outbox SET created_at = $2 WHERE id = $1`, pendingID, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("backdate pending created_at: %v", err)
	}

	removed, err := repo.DeletePublishedBefore(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeletePublishedBefore: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeletePublishedBefore = %d, want 1", removed)
	}

	var oldCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE id = $1`, oldID).Scan(&oldCount); err != nil {
		t.Fatalf("count old event: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("old published event still present, want deleted")
	}

	claimed, err := repo.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after cleanup: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != pendingID {
		t.Errorf("ClaimPending after cleanup = %+v, want only pending %s", claimed, pendingID)
	}

	var recentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE id = $1`, recentID).Scan(&recentCount); err != nil {
		t.Fatalf("count recent event: %v", err)
	}
	if recentCount != 1 {
		t.Errorf("recent published event count = %d, want 1", recentCount)
	}
}
