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

func createTestMedia(t *testing.T, repo storage.MediaRepository, instanceID uuid.UUID, messageID, storagePath string, expiresAt time.Time) *model.Media {
	t.Helper()

	record, err := repo.Create(context.Background(), model.Media{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		Direction:   "inbound",
		MessageID:   messageID,
		Mimetype:    "image/jpeg",
		Filename:    "foto.jpg",
		SizeBytes:   1234,
		StoragePath: storagePath,
		SHA256:      "c0ffee",
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		t.Fatalf("create media %s: %v", storagePath, err)
	}
	return record
}

func TestMediaRepositoryCreateAndGet(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instance := createTestInstance(t, instances, "media", "")
	start := time.Now()
	expiresAt := start.Add(2 * time.Hour)
	messageID := uuid.NewString()

	created, err := media.Create(ctx, model.Media{
		ID:          uuid.New(),
		InstanceID:  instance.ID,
		Direction:   "inbound",
		MessageID:   messageID,
		Mimetype:    "image/jpeg",
		Filename:    "foto.jpg",
		SizeBytes:   1234,
		StoragePath: "media/" + instance.ID.String() + "/1",
		SHA256:      "abc123",
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.ID == uuid.Nil {
		t.Error("Create: ID is nil")
	}
	if created.InstanceID != instance.ID {
		t.Errorf("Create: InstanceID = %s, want %s", created.InstanceID, instance.ID)
	}
	if created.Direction != "inbound" || created.Mimetype != "image/jpeg" || created.Filename != "foto.jpg" {
		t.Errorf("Create: direction/mimetype/filename = %q/%q/%q", created.Direction, created.Mimetype, created.Filename)
	}
	if created.MessageID != messageID {
		t.Errorf("Create: MessageID = %q, want %q", created.MessageID, messageID)
	}
	if created.SizeBytes != 1234 || created.SHA256 != "abc123" {
		t.Errorf("Create: size/sha256 = %d/%q, want 1234/abc123", created.SizeBytes, created.SHA256)
	}
	if created.StoragePath != "media/"+instance.ID.String()+"/1" {
		t.Errorf("Create: StoragePath = %q", created.StoragePath)
	}
	requireTimeBetween(t, "Create: CreatedAt", created.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
	requireTimeNear(t, "Create: ExpiresAt", created.ExpiresAt, expiresAt)

	got, err := media.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.InstanceID != instance.ID || got.MessageID != messageID {
		t.Errorf("Get: %+v, want the created record %s", got, created.ID)
	}
	if got.StoragePath != created.StoragePath || got.SHA256 != created.SHA256 {
		t.Errorf("Get: path/sha256 = %q/%q, want %q/%q", got.StoragePath, got.SHA256, created.StoragePath, created.SHA256)
	}
	requireTimeNear(t, "Get: ExpiresAt", got.ExpiresAt, expiresAt)
}

func TestMediaRepositoryCreateEmptyOptionals(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instance := createTestInstance(t, instances, "media-nullable", "")

	created, err := media.Create(ctx, model.Media{
		ID:          uuid.New(),
		InstanceID:  instance.ID,
		Direction:   "outbound",
		Mimetype:    "application/pdf",
		SizeBytes:   8,
		StoragePath: "media/" + instance.ID.String() + "/2",
		SHA256:      "def456",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.MessageID != "" {
		t.Errorf("Create: MessageID = %q, want empty", created.MessageID)
	}
	if created.Filename != "" {
		t.Errorf("Create: Filename = %q, want empty", created.Filename)
	}

	got, err := media.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MessageID != "" || got.Filename != "" {
		t.Errorf("Get: MessageID/Filename = %q/%q, want empty", got.MessageID, got.Filename)
	}
}

func TestMediaRepositoryGetNotFound(t *testing.T) {
	pool := newTestPool(t)
	media := NewMediaRepository(pool)

	if _, err := media.Get(context.Background(), uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get error = %v, want ErrNotFound", err)
	}
}

func TestMediaRepositoryCreateUnknownInstance(t *testing.T) {
	pool := newTestPool(t)
	media := NewMediaRepository(pool)

	_, err := media.Create(context.Background(), model.Media{
		ID:          uuid.New(),
		InstanceID:  uuid.New(),
		Direction:   "inbound",
		Mimetype:    "image/jpeg",
		SizeBytes:   1,
		StoragePath: "media/unknown/1",
		SHA256:      "abc",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Create error = %v, want ErrNotFound", err)
	}
}

func TestMediaRepositoryListByInstance(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instanceA := createTestInstance(t, instances, "media-a", "")
	instanceB := createTestInstance(t, instances, "media-b", "")
	now := time.Now()

	createTestMedia(t, media, instanceA.ID, "wa-1", "media/a/1", now.Add(time.Hour))
	createTestMedia(t, media, instanceA.ID, "wa-2", "media/a/2", now.Add(time.Hour))
	createTestMedia(t, media, instanceB.ID, "wa-3", "media/b/1", now.Add(time.Hour))

	found, err := media.ListByInstance(ctx, instanceA.ID)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("ListByInstance returned %d records, want 2", len(found))
	}
	for _, record := range found {
		if record.InstanceID != instanceA.ID {
			t.Errorf("ListByInstance returned instance %s, want %s", record.InstanceID, instanceA.ID)
		}
	}

	empty, err := media.ListByInstance(ctx, uuid.New())
	if err != nil {
		t.Fatalf("ListByInstance unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListByInstance unknown returned %d records, want 0", len(empty))
	}
}

func TestMediaRepositoryListExpired(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instance := createTestInstance(t, instances, "media-expired", "")
	now := time.Now()

	due := createTestMedia(t, media, instance.ID, "wa-old", "media/e/old", now.Add(-time.Hour))
	alsoDue := createTestMedia(t, media, instance.ID, "wa-older", "media/e/older", now.Add(-2*time.Minute))
	createTestMedia(t, media, instance.ID, "wa-fresh", "media/e/fresh", now.Add(time.Hour))

	found, err := media.ListExpired(ctx, now)
	if err != nil {
		t.Fatalf("ListExpired: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("ListExpired returned %d records, want 2", len(found))
	}
	if found[0].ID != due.ID || found[1].ID != alsoDue.ID {
		t.Errorf("ListExpired order = %s/%s, want oldest first %s/%s", found[0].ID, found[1].ID, due.ID, alsoDue.ID)
	}
}

func TestMediaRepositoryDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instance := createTestInstance(t, instances, "media-delete", "")
	record := createTestMedia(t, media, instance.ID, "wa-1", "media/d/1", time.Now().Add(time.Hour))

	if err := media.Delete(ctx, record.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := media.Get(ctx, record.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get after Delete error = %v, want ErrNotFound", err)
	}
	if err := media.Delete(ctx, record.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("second Delete error = %v, want ErrNotFound", err)
	}
}

func TestMediaRepositoryDeleteByInstance(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	media := NewMediaRepository(pool)
	instanceA := createTestInstance(t, instances, "media-del-a", "")
	instanceB := createTestInstance(t, instances, "media-del-b", "")

	createTestMedia(t, media, instanceA.ID, "wa-1", "media/da/1", time.Now().Add(time.Hour))
	createTestMedia(t, media, instanceA.ID, "wa-2", "media/da/2", time.Now().Add(time.Hour))
	kept := createTestMedia(t, media, instanceB.ID, "wa-3", "media/db/1", time.Now().Add(time.Hour))

	removed, err := media.DeleteByInstance(ctx, instanceA.ID)
	if err != nil {
		t.Fatalf("DeleteByInstance: %v", err)
	}
	if removed != 2 {
		t.Errorf("DeleteByInstance removed = %d, want 2", removed)
	}

	remaining, err := media.ListByInstance(ctx, instanceA.ID)
	if err != nil {
		t.Fatalf("ListByInstance after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("instance A still has %d media records", len(remaining))
	}
	if _, err := media.Get(ctx, kept.ID); err != nil {
		t.Errorf("instance B media was removed: %v", err)
	}
}
