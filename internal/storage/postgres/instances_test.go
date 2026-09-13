package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
	"wzap/internal/storage/postgres/postgrestest"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := postgrestest.NewPool(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	return pool
}

func createTestInstance(t *testing.T, repo storage.InstanceRepository, name, externalRef string) *model.Instance {
	t.Helper()

	instance, err := repo.Create(context.Background(), model.Instance{
		ID:          uuid.New(),
		Name:        name,
		ExternalRef: externalRef,
		Status:      "disconnected",
	})
	if err != nil {
		t.Fatalf("create instance %q: %v", name, err)
	}
	return instance
}

func requireTimeBetween(t *testing.T, label string, got, start, end time.Time) {
	t.Helper()

	if got.IsZero() {
		t.Errorf("%s: time is zero", label)
		return
	}
	if got.Before(start) || got.After(end) {
		t.Errorf("%s: got %s, want between %s and %s", label, got.UTC(), start.UTC(), end.UTC())
	}
}

func requireTimeNear(t *testing.T, label string, got, want time.Time) {
	t.Helper()

	if got.IsZero() {
		t.Errorf("%s: time is zero", label)
		return
	}
	if diff := got.Sub(want); diff < -time.Second || diff > time.Second {
		t.Errorf("%s: got %s, want %s (±1s)", label, got.UTC(), want.UTC())
	}
}

func requireTimePtrNear(t *testing.T, label string, got *time.Time, want time.Time) {
	t.Helper()

	if got == nil {
		t.Errorf("%s: time pointer is nil", label)
		return
	}
	requireTimeNear(t, label, *got, want)
}

func TestInstanceRepositoryCreate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)
	start := time.Now()

	created, err := repo.Create(ctx, model.Instance{
		ID:          uuid.New(),
		Name:        "Account A",
		ExternalRef: "account-a",
		Status:      "disconnected",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.ID == uuid.Nil {
		t.Error("Create: ID is nil")
	}
	if created.Name != "Account A" {
		t.Errorf("Create: Name = %q, want %q", created.Name, "Account A")
	}
	if created.ExternalRef != "account-a" {
		t.Errorf("Create: ExternalRef = %q, want %q", created.ExternalRef, "account-a")
	}
	if created.Status != "disconnected" {
		t.Errorf("Create: Status = %q, want %q", created.Status, "disconnected")
	}
	if created.WhatsAppJID != "" {
		t.Errorf("Create: WhatsAppJID = %q, want empty", created.WhatsAppJID)
	}
	if created.LastConnectedAt != nil {
		t.Errorf("Create: LastConnectedAt = %v, want nil", created.LastConnectedAt)
	}
	if created.LastError != "" {
		t.Errorf("Create: LastError = %q, want empty", created.LastError)
	}
	requireTimeBetween(t, "Create: CreatedAt", created.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
	requireTimeBetween(t, "Create: UpdatedAt", created.UpdatedAt, start.Add(-time.Second), time.Now().Add(time.Second))

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count instance: %v", err)
	}
	if count != 1 {
		t.Errorf("instances with created id = %d, want 1", count)
	}
}

func TestInstanceRepositoryCreateDuplicateExternalRef(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	createTestInstance(t, repo, "first", "same-ref")

	_, err := repo.Create(ctx, model.Instance{
		ID:          uuid.New(),
		Name:        "second",
		ExternalRef: "same-ref",
		Status:      "disconnected",
	})
	if !errors.Is(err, storage.ErrExternalRefTaken) {
		t.Fatalf("Create duplicate external_ref error = %v, want ErrExternalRefTaken", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE external_ref = 'same-ref'`).Scan(&count); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	if count != 1 {
		t.Errorf("instances with external_ref = %d, want 1", count)
	}
}

func TestInstanceRepositoryCreateWithoutExternalRef(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	createTestInstance(t, repo, "first", "")
	createTestInstance(t, repo, "second", "")

	var nulls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM instances WHERE external_ref IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count null external_ref: %v", err)
	}
	if nulls != 2 {
		t.Errorf("instances with NULL external_ref = %d, want 2", nulls)
	}

	_, err := repo.GetByExternalRef(ctx, "")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByExternalRef(%q) error = %v, want ErrNotFound", "", err)
	}
}

func TestInstanceRepositoryGet(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	created := createTestInstance(t, repo, "Account A", "account-a")

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.ExternalRef != created.ExternalRef {
		t.Errorf("Get returned %+v, want %+v", got, created)
	}

	_, err = repo.Get(ctx, uuid.New())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInstanceRepositoryGetByExternalRef(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	created := createTestInstance(t, repo, "Account A", "account-a")

	got, err := repo.GetByExternalRef(ctx, "account-a")
	if err != nil {
		t.Fatalf("GetByExternalRef: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetByExternalRef ID = %s, want %s", got.ID, created.ID)
	}

	_, err = repo.GetByExternalRef(ctx, "missing-ref")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByExternalRef(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInstanceRepositoryList(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	empty, cursor, err := repo.List(ctx, 10, "")
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List(empty) returned %d instances, want 0", len(empty))
	}
	if cursor != "" {
		t.Errorf("List(empty) cursor = %q, want empty", cursor)
	}

	zero, cursor, err := repo.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("List(limit 0): %v", err)
	}
	if len(zero) != 0 || cursor != "" {
		t.Errorf("List(limit 0) = %d items, cursor %q; want 0 items and empty cursor", len(zero), cursor)
	}

	first := createTestInstance(t, repo, "first", "")
	second := createTestInstance(t, repo, "second", "")
	third := createTestInstance(t, repo, "third", "")

	page1, cursor, err := repo.List(ctx, 2, "")
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("List page 1 returned %d instances, want 2", len(page1))
	}
	if page1[0].ID != third.ID || page1[1].ID != second.ID {
		t.Errorf("List page 1 order = [%s %s], want [%s %s]", page1[0].ID, page1[1].ID, third.ID, second.ID)
	}
	if cursor != second.ID.String() {
		t.Errorf("List page 1 cursor = %q, want %q", cursor, second.ID)
	}

	page2, cursor, err := repo.List(ctx, 2, cursor)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != first.ID {
		t.Errorf("List page 2 = %+v, want only %s", page2, first.ID)
	}
	if cursor != "" {
		t.Errorf("List page 2 cursor = %q, want empty", cursor)
	}
}

func TestInstanceRepositoryListInvalidCursor(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	_, _, err := repo.List(ctx, 10, "not-a-uuid")
	if !errors.Is(err, storage.ErrInvalidCursor) {
		t.Errorf("List(invalid cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestInstanceRepositoryUpdate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	instance := createTestInstance(t, repo, "original", "original-ref")

	connectedAt := time.Now().Add(-time.Minute).UTC()
	instance.Name = "renamed"
	instance.ExternalRef = "renamed-ref"
	instance.Status = "connected"
	instance.WhatsAppJID = "5511999999999@s.whatsapp.net"
	instance.LastError = "previous failure"
	instance.LastConnectedAt = &connectedAt

	updated, err := repo.Update(ctx, *instance)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" || updated.ExternalRef != "renamed-ref" {
		t.Errorf("Update returned %+v", updated)
	}
	if updated.Status != "connected" {
		t.Errorf("Update Status = %q, want connected", updated.Status)
	}
	if updated.WhatsAppJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("Update WhatsAppJID = %q", updated.WhatsAppJID)
	}
	if updated.LastError != "previous failure" {
		t.Errorf("Update LastError = %q", updated.LastError)
	}
	requireTimePtrNear(t, "Update: LastConnectedAt", updated.LastConnectedAt, connectedAt)

	updated.ExternalRef = ""
	updated.WhatsAppJID = ""
	updated.LastError = ""
	updated.LastConnectedAt = nil

	cleared, err := repo.Update(ctx, *updated)
	if err != nil {
		t.Fatalf("Update(clear): %v", err)
	}
	if cleared.ExternalRef != "" || cleared.WhatsAppJID != "" || cleared.LastError != "" || cleared.LastConnectedAt != nil {
		t.Errorf("Update(clear) did not clear optional fields: %+v", cleared)
	}

	createTestInstance(t, repo, "reuses ref", "renamed-ref")
}

func TestInstanceRepositoryUpdateDuplicateExternalRef(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	first := createTestInstance(t, repo, "first", "first-ref")
	second := createTestInstance(t, repo, "second", "second-ref")

	second.ExternalRef = first.ExternalRef
	_, err := repo.Update(ctx, *second)
	if !errors.Is(err, storage.ErrExternalRefTaken) {
		t.Fatalf("Update duplicate external_ref error = %v, want ErrExternalRefTaken", err)
	}

	got, err := repo.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("Get after failed update: %v", err)
	}
	if got.ExternalRef != "second-ref" {
		t.Errorf("external_ref changed to %q after failed update, want second-ref", got.ExternalRef)
	}
}

func TestInstanceRepositoryUpdateNotFound(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	_, err := repo.Update(ctx, model.Instance{
		ID:     uuid.New(),
		Name:   "ghost",
		Status: "disconnected",
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Update(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInstanceRepositorySetConnection(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	instance := createTestInstance(t, repo, "original", "original-ref")
	connectedAt := time.Now().Add(-time.Minute).UTC()
	instance.Status = "connected"
	instance.WhatsAppJID = "5511999999999@s.whatsapp.net"
	instance.LastError = "previous failure"
	instance.LastConnectedAt = &connectedAt
	if _, err := repo.Update(ctx, *instance); err != nil {
		t.Fatalf("Update seed: %v", err)
	}

	if err := repo.SetConnection(ctx, instance.ID, "disconnected", ""); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}

	got, err := repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get after SetConnection: %v", err)
	}
	if got.Status != "disconnected" {
		t.Errorf("status = %q, want disconnected", got.Status)
	}
	if got.WhatsAppJID != "" {
		t.Errorf("whatsapp_jid = %q, want empty", got.WhatsAppJID)
	}
	if got.Name != "original" || got.ExternalRef != "original-ref" {
		t.Errorf("SetConnection touched identity fields: %+v", got)
	}
	if got.LastError != "previous failure" {
		t.Errorf("last_error = %q, want the stored previous failure", got.LastError)
	}
	requireTimePtrNear(t, "SetConnection: LastConnectedAt", got.LastConnectedAt, connectedAt)

	if err := repo.SetConnection(ctx, instance.ID, "connected", "5511888888888@s.whatsapp.net"); err != nil {
		t.Fatalf("SetConnection(connected): %v", err)
	}
	got, err = repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get after SetConnection(connected): %v", err)
	}
	if got.Status != "connected" || got.WhatsAppJID != "5511888888888@s.whatsapp.net" {
		t.Errorf("connected state = %+v, want the new status and JID", got)
	}

	if err := repo.SetConnection(ctx, uuid.New(), "disconnected", ""); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("SetConnection(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInstanceRepositorySetConnectionState(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	instance := createTestInstance(t, repo, "original", "original-ref")
	connectedAt := time.Now().Add(-time.Minute).UTC()
	instance.Status = "connected"
	instance.WhatsAppJID = "5511999999999@s.whatsapp.net"
	instance.LastConnectedAt = &connectedAt
	if _, err := repo.Update(ctx, *instance); err != nil {
		t.Fatalf("Update seed: %v", err)
	}

	// An error transition with an empty JID keeps the stored JID and
	// last_connected_at, and replaces last_error.
	if err := repo.SetConnectionState(ctx, instance.ID, "error", "", "temporary ban", nil); err != nil {
		t.Fatalf("SetConnectionState: %v", err)
	}
	got, err := repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get after SetConnectionState: %v", err)
	}
	if got.Status != "error" || got.LastError != "temporary ban" {
		t.Errorf("state = %+v, want status error with the reason", got)
	}
	if got.WhatsAppJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("whatsapp_jid = %q, want the stored JID kept", got.WhatsAppJID)
	}
	if got.Name != "original" || got.ExternalRef != "original-ref" {
		t.Errorf("SetConnectionState touched identity fields: %+v", got)
	}
	requireTimePtrNear(t, "SetConnectionState: LastConnectedAt", got.LastConnectedAt, connectedAt)

	// A connected transition stamps last_connected_at and clears last_error.
	newConnectedAt := time.Now().UTC()
	if err := repo.SetConnectionState(ctx, instance.ID, "connected", "5511888888888@s.whatsapp.net", "", &newConnectedAt); err != nil {
		t.Fatalf("SetConnectionState(connected): %v", err)
	}
	got, err = repo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get after SetConnectionState(connected): %v", err)
	}
	if got.Status != "connected" || got.WhatsAppJID != "5511888888888@s.whatsapp.net" {
		t.Errorf("connected state = %+v, want the new status and JID", got)
	}
	if got.LastError != "" {
		t.Errorf("last_error = %q, want empty after a clean connect", got.LastError)
	}
	requireTimePtrNear(t, "SetConnectionState(connected): LastConnectedAt", got.LastConnectedAt, newConnectedAt)

	if err := repo.SetConnectionState(ctx, uuid.New(), "disconnected", "", "", nil); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("SetConnectionState(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInstanceRepositoryDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)

	instance := createTestInstance(t, repo, "to delete", "delete-ref")
	message := createTestMessage(t, messages, instance.ID, `{"text":"hi"}`)

	if err := repo.Delete(ctx, instance.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := repo.Get(ctx, instance.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get after Delete error = %v, want ErrNotFound", err)
	}
	if _, err := messages.Get(ctx, message.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("message Get after instance Delete error = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, instance.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Delete twice error = %v, want ErrNotFound", err)
	}
}
