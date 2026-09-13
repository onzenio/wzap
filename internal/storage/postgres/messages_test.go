package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/model"
	"wzap/internal/storage"
)

func createTestMessage(t *testing.T, repo storage.MessageRepository, instanceID uuid.UUID, payload string) *model.OutboundMessage {
	t.Helper()

	message, err := repo.Create(context.Background(), model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		Type:         "text",
		RecipientJID: "5511999999999@s.whatsapp.net",
		Payload:      []byte(payload),
		Status:       "queued",
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	return message
}

func setNextAttemptAt(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, at time.Time) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), `UPDATE message_queue SET next_attempt_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("set next_attempt_at: %v", err)
	}
}

func backdateUpdatedAt(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, at time.Time) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), `UPDATE message_queue SET updated_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}
}

func requireJSONEqual(t *testing.T, label string, got, want []byte) {
	t.Helper()

	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Errorf("%s: unmarshal %q: %v", label, got, err)
		return
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Errorf("%s: unmarshal %q: %v", label, want, err)
		return
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("%s: got %s, want %s", label, got, want)
	}
}

func TestMessageRepositoryCreate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	start := time.Now()

	message, err := messages.Create(ctx, model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   instance.ID,
		Type:         "text",
		RecipientJID: "5511999999999@s.whatsapp.net",
		Payload:      []byte(`{"text":"olá"}`),
		Status:       "queued",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if message.ID == uuid.Nil {
		t.Error("Create: ID is nil")
	}
	if message.InstanceID != instance.ID {
		t.Errorf("Create: InstanceID = %s, want %s", message.InstanceID, instance.ID)
	}
	if message.Type != "text" || message.RecipientJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("Create: type/recipient = %q/%q", message.Type, message.RecipientJID)
	}
	requireJSONEqual(t, "Create: Payload", message.Payload, []byte(`{"text":"olá"}`))
	if message.MediaID != nil {
		t.Errorf("Create: MediaID = %v, want nil", message.MediaID)
	}
	if message.Status != "queued" {
		t.Errorf("Create: Status = %q, want queued", message.Status)
	}
	if message.WhatsAppMessageID != "" || message.LastError != "" {
		t.Errorf("Create: WhatsAppMessageID/LastError = %q/%q, want empty", message.WhatsAppMessageID, message.LastError)
	}
	if message.Attempts != 0 {
		t.Errorf("Create: Attempts = %d, want 0", message.Attempts)
	}
	if message.DeliveredAt != nil || message.ReadAt != nil {
		t.Errorf("Create: DeliveredAt/ReadAt = %v/%v, want nil", message.DeliveredAt, message.ReadAt)
	}
	requireTimeBetween(t, "Create: CreatedAt", message.CreatedAt, start.Add(-time.Second), time.Now().Add(time.Second))
	requireTimeBetween(t, "Create: UpdatedAt", message.UpdatedAt, start.Add(-time.Second), time.Now().Add(time.Second))

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_queue WHERE id = $1`, message.ID).Scan(&count); err != nil {
		t.Fatalf("count message: %v", err)
	}
	if count != 1 {
		t.Errorf("message_queue with created id = %d, want 1", count)
	}
}

func TestMessageRepositoryCreateWithMedia(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	mediaID := uuid.New()

	message, err := messages.Create(ctx, model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   instance.ID,
		Type:         "image",
		RecipientJID: "5511999999999@s.whatsapp.net",
		Payload:      []byte(`{"caption":"foto"}`),
		MediaID:      &mediaID,
		Status:       "queued",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if message.MediaID == nil || *message.MediaID != mediaID {
		t.Errorf("Create: MediaID = %v, want %s", message.MediaID, mediaID)
	}

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MediaID == nil || *got.MediaID != mediaID {
		t.Errorf("Get: MediaID = %v, want %s", got.MediaID, mediaID)
	}
}

func TestMessageRepositoryCreateUnknownInstance(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	messages := NewMessageRepository(pool)

	_, err := messages.Create(ctx, model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   uuid.New(),
		Type:         "text",
		RecipientJID: "5511999999999@s.whatsapp.net",
		Payload:      []byte(`{"text":"hi"}`),
		Status:       "queued",
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Create(unknown instance) error = %v, want ErrNotFound", err)
	}
}

func TestMessageRepositoryGet(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	created := createTestMessage(t, messages, instance.ID, `{"text":"hello"}`)

	got, err := messages.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.Status != "queued" {
		t.Errorf("Get returned %+v, want id %s status queued", got, created.ID)
	}
	requireJSONEqual(t, "Get: Payload", got.Payload, created.Payload)

	_, err = messages.Get(ctx, uuid.New())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestMessageRepositoryListByInstance(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	other := createTestInstance(t, instances, "other", "")

	first := createTestMessage(t, messages, instance.ID, `{"text":"1"}`)
	second := createTestMessage(t, messages, instance.ID, `{"text":"2"}`)
	third := createTestMessage(t, messages, instance.ID, `{"text":"3"}`)
	createTestMessage(t, messages, other.ID, `{"text":"other"}`)

	zero, cursor, err := messages.ListByInstance(ctx, instance.ID, 0, "")
	if err != nil {
		t.Fatalf("ListByInstance(limit 0): %v", err)
	}
	if len(zero) != 0 || cursor != "" {
		t.Errorf("ListByInstance(limit 0) = %d messages, cursor %q; want 0 messages and empty cursor", len(zero), cursor)
	}

	page1, cursor, err := messages.ListByInstance(ctx, instance.ID, 2, "")
	if err != nil {
		t.Fatalf("ListByInstance page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("ListByInstance page 1 returned %d messages, want 2", len(page1))
	}
	if page1[0].ID != third.ID || page1[1].ID != second.ID {
		t.Errorf("ListByInstance page 1 order = [%s %s], want [%s %s]", page1[0].ID, page1[1].ID, third.ID, second.ID)
	}
	if cursor != second.ID.String() {
		t.Errorf("ListByInstance page 1 cursor = %q, want %q", cursor, second.ID)
	}

	page2, cursor, err := messages.ListByInstance(ctx, instance.ID, 2, cursor)
	if err != nil {
		t.Fatalf("ListByInstance page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != first.ID {
		t.Errorf("ListByInstance page 2 = %+v, want only %s", page2, first.ID)
	}
	if cursor != "" {
		t.Errorf("ListByInstance page 2 cursor = %q, want empty", cursor)
	}

	otherPage, cursor, err := messages.ListByInstance(ctx, other.ID, 10, "")
	if err != nil {
		t.Fatalf("ListByInstance(other): %v", err)
	}
	if len(otherPage) != 1 {
		t.Errorf("ListByInstance(other) returned %d messages, want 1", len(otherPage))
	}
	if cursor != "" {
		t.Errorf("ListByInstance(other) cursor = %q, want empty", cursor)
	}

	if _, _, err := messages.ListByInstance(ctx, instance.ID, 10, "not-a-uuid"); !errors.Is(err, storage.ErrInvalidCursor) {
		t.Errorf("ListByInstance(invalid cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestMessageRepositoryClaimQueued(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	first := createTestMessage(t, messages, instance.ID, `{"text":"1"}`)
	second := createTestMessage(t, messages, instance.ID, `{"text":"2"}`)
	third := createTestMessage(t, messages, instance.ID, `{"text":"3"}`)
	future := createTestMessage(t, messages, instance.ID, `{"text":"future"}`)
	setNextAttemptAt(t, pool, future.ID, time.Now().Add(time.Hour))

	start := time.Now()
	claimed, err := messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("ClaimQueued returned %d messages, want 3", len(claimed))
	}
	wantOrder := []uuid.UUID{first.ID, second.ID, third.ID}
	for i, message := range claimed {
		if message.ID != wantOrder[i] {
			t.Errorf("ClaimQueued[%d] = %s, want %s", i, message.ID, wantOrder[i])
		}
		if message.Status != "sending" {
			t.Errorf("ClaimQueued[%d] status = %q, want sending", i, message.Status)
		}
	}
	requireTimeBetween(t, "ClaimQueued[0]: UpdatedAt", claimed[0].UpdatedAt, start.Add(-time.Second), time.Now().Add(time.Second))

	again, err := messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued second call: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("ClaimQueued second call returned %d messages, want 0", len(again))
	}

	storedFuture, err := messages.Get(ctx, future.ID)
	if err != nil {
		t.Fatalf("Get future: %v", err)
	}
	if storedFuture.Status != "queued" {
		t.Errorf("future message status = %q, want queued", storedFuture.Status)
	}
}

func TestMessageRepositoryClaimQueuedLimit(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	first := createTestMessage(t, messages, instance.ID, `{"text":"1"}`)
	second := createTestMessage(t, messages, instance.ID, `{"text":"2"}`)
	createTestMessage(t, messages, instance.ID, `{"text":"3"}`)

	claimed, err := messages.ClaimQueued(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimQueued(2): %v", err)
	}
	if len(claimed) != 2 || claimed[0].ID != first.ID || claimed[1].ID != second.ID {
		t.Errorf("ClaimQueued(2) = %+v, want [%s %s]", claimed, first.ID, second.ID)
	}

	claimed, err = messages.ClaimQueued(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimQueued(2) second call: %v", err)
	}
	if len(claimed) != 1 {
		t.Errorf("ClaimQueued(2) second call returned %d messages, want 1", len(claimed))
	}

	claimed, err = messages.ClaimQueued(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimQueued(2) third call: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimQueued(2) third call returned %d messages, want 0", len(claimed))
	}
}

func TestMessageRepositoryClaimQueuedConcurrent(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	const total = 10
	for i := 0; i < total; i++ {
		createTestMessage(t, messages, instance.ID, `{"text":"concurrent"}`)
	}

	start := make(chan struct{})
	results := make([][]model.OutboundMessage, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup

	for i := 0; i < len(results); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = messages.ClaimQueued(ctx, total)
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[uuid.UUID]bool, total)
	for i, batch := range results {
		if errs[i] != nil {
			t.Fatalf("ClaimQueued goroutine %d: %v", i, errs[i])
		}
		for _, message := range batch {
			if seen[message.ID] {
				t.Errorf("message %s was claimed by both goroutines", message.ID)
			}
			seen[message.ID] = true
			if message.Status != "sending" {
				t.Errorf("message %s status = %q, want sending", message.ID, message.Status)
			}
		}
	}
	if len(seen) != total {
		t.Errorf("claimed %d distinct messages, want %d", len(seen), total)
	}
}

func TestMessageRepositoryClaimQueuedSkipsLockedRows(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	locked := createTestMessage(t, messages, instance.ID, `{"text":"locked"}`)
	free := createTestMessage(t, messages, instance.ID, `{"text":"free"}`)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT id FROM message_queue WHERE id = $1 FOR UPDATE`, locked.ID); err != nil {
		t.Fatalf("lock row: %v", err)
	}

	claimed, err := messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued with locked row: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != free.ID {
		t.Fatalf("ClaimQueued = %+v, want only %s", claimed, free.ID)
	}

	gotLocked, err := messages.Get(ctx, locked.ID)
	if err != nil {
		t.Fatalf("Get locked: %v", err)
	}
	if gotLocked.Status != "queued" {
		t.Errorf("locked message status = %q, want queued", gotLocked.Status)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock transaction: %v", err)
	}

	claimed, err = messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued after unlock: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != locked.ID {
		t.Errorf("ClaimQueued after unlock = %+v, want [%s]", claimed, locked.ID)
	}
}

func TestMessageRepositoryClaimQueuedIgnoresPastAttempts(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	due := createTestMessage(t, messages, instance.ID, `{"text":"due"}`)
	setNextAttemptAt(t, pool, due.ID, time.Now().Add(-time.Minute))

	claimed, err := messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != due.ID {
		t.Errorf("ClaimQueued = %+v, want [%s]", claimed, due.ID)
	}
}

func TestMessageRepositoryMarkSent(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	message := createTestMessage(t, messages, instance.ID, `{"text":"hi"}`)

	if _, err := messages.ClaimQueued(ctx, 1); err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}

	start := time.Now()
	if err := messages.MarkSent(ctx, message.ID, "wamid.SENT"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after MarkSent: %v", err)
	}
	if got.Status != "sent" {
		t.Errorf("Status = %q, want sent", got.Status)
	}
	if got.WhatsAppMessageID != "wamid.SENT" {
		t.Errorf("WhatsAppMessageID = %q, want wamid.SENT", got.WhatsAppMessageID)
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty", got.LastError)
	}
	requireTimeBetween(t, "MarkSent: UpdatedAt", got.UpdatedAt, start.Add(-time.Second), time.Now().Add(time.Second))

	if err := messages.MarkSent(ctx, uuid.New(), "wamid.SENT"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MarkSent(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestMessageRepositoryMarkFailed(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	message := createTestMessage(t, messages, instance.ID, `{"text":"hi"}`)

	if err := messages.MarkFailed(ctx, message.ID, "recipient not registered"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after MarkFailed: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.LastError != "recipient not registered" {
		t.Errorf("LastError = %q, want recipient not registered", got.LastError)
	}

	if err := messages.MarkFailed(ctx, uuid.New(), "boom"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MarkFailed(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestMessageRepositoryMarkRetrying(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	message := createTestMessage(t, messages, instance.ID, `{"text":"hi"}`)

	nextAttemptAt := time.Now().Add(time.Minute)
	if err := messages.MarkRetrying(ctx, message.ID, "timeout", nextAttemptAt); err != nil {
		t.Fatalf("MarkRetrying: %v", err)
	}

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after MarkRetrying: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("Status = %q, want queued", got.Status)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
	if got.LastError != "timeout" {
		t.Errorf("LastError = %q, want timeout", got.LastError)
	}

	claimed, err := messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued before next attempt: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimQueued returned %d messages before next_attempt_at, want 0", len(claimed))
	}

	if err := messages.MarkRetrying(ctx, message.ID, "timeout again", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("MarkRetrying second call: %v", err)
	}
	got, err = messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after second MarkRetrying: %v", err)
	}
	if got.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", got.Attempts)
	}

	claimed, err = messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued after due: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != message.ID {
		t.Errorf("ClaimQueued after due = %+v, want [%s]", claimed, message.ID)
	}

	if err := messages.MarkRetrying(ctx, uuid.New(), "timeout", time.Now()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MarkRetrying(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestMessageRepositoryUpdateReceipt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")
	message := createTestMessage(t, messages, instance.ID, `{"text":"hi"}`)

	if _, err := messages.ClaimQueued(ctx, 1); err != nil {
		t.Fatalf("ClaimQueued: %v", err)
	}
	if err := messages.MarkSent(ctx, message.ID, "wamid.RECEIPTS"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	deliveredAt := time.Now().Add(-2 * time.Minute).UTC()
	updated, err := messages.UpdateReceipt(ctx, "wamid.RECEIPTS", "delivered", deliveredAt)
	if err != nil {
		t.Fatalf("UpdateReceipt(delivered): %v", err)
	}
	if !updated {
		t.Error("UpdateReceipt(delivered) = false, want true")
	}

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after delivered: %v", err)
	}
	requireTimePtrNear(t, "DeliveredAt", got.DeliveredAt, deliveredAt)
	if got.ReadAt != nil {
		t.Errorf("ReadAt = %v, want nil", got.ReadAt)
	}

	readAt := deliveredAt.Add(30 * time.Second)
	updated, err = messages.UpdateReceipt(ctx, "wamid.RECEIPTS", "read", readAt)
	if err != nil {
		t.Fatalf("UpdateReceipt(read): %v", err)
	}
	if !updated {
		t.Error("UpdateReceipt(read) = false, want true")
	}

	got, err = messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after read: %v", err)
	}
	requireTimePtrNear(t, "ReadAt", got.ReadAt, readAt)
	requireTimePtrNear(t, "DeliveredAt after read", got.DeliveredAt, deliveredAt)

	playedAt := readAt.Add(time.Minute)
	updated, err = messages.UpdateReceipt(ctx, "wamid.RECEIPTS", "played", playedAt)
	if err != nil {
		t.Fatalf("UpdateReceipt(played): %v", err)
	}
	if !updated {
		t.Error("UpdateReceipt(played) = false, want true")
	}

	got, err = messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get after played: %v", err)
	}
	requireTimePtrNear(t, "ReadAt after played", got.ReadAt, readAt)

	updated, err = messages.UpdateReceipt(ctx, "wamid.UNKNOWN", "read", readAt)
	if err != nil {
		t.Fatalf("UpdateReceipt(unknown message): %v", err)
	}
	if updated {
		t.Error("UpdateReceipt(unknown message) = true, want false")
	}

	updated, err = messages.UpdateReceipt(ctx, "wamid.RECEIPTS", "server", readAt)
	if err != nil {
		t.Fatalf("UpdateReceipt(unknown status): %v", err)
	}
	if updated {
		t.Error("UpdateReceipt(unknown status) = true, want false")
	}
}

func TestMessageRepositoryRequeueStuck(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	instances := NewInstanceRepository(pool)
	messages := NewMessageRepository(pool)
	instance := createTestInstance(t, instances, "messages", "")

	stuck := createTestMessage(t, messages, instance.ID, `{"text":"stuck"}`)
	fresh := createTestMessage(t, messages, instance.ID, `{"text":"fresh"}`)

	claimed, err := messages.ClaimQueued(ctx, 1)
	if err != nil {
		t.Fatalf("ClaimQueued stuck: %v", err)
	}
	claimedFresh, err := messages.ClaimQueued(ctx, 1)
	if err != nil {
		t.Fatalf("ClaimQueued fresh: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != stuck.ID || len(claimedFresh) != 1 || claimedFresh[0].ID != fresh.ID {
		t.Fatalf("claim setup = %+v / %+v, want [%s] / [%s]", claimed, claimedFresh, stuck.ID, fresh.ID)
	}

	backdateUpdatedAt(t, pool, stuck.ID, time.Now().Add(-10*time.Minute))

	requeued, err := messages.RequeueStuck(ctx, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("RequeueStuck: %v", err)
	}
	if requeued != 1 {
		t.Errorf("RequeueStuck = %d, want 1", requeued)
	}

	gotStuck, err := messages.Get(ctx, stuck.ID)
	if err != nil {
		t.Fatalf("Get stuck: %v", err)
	}
	if gotStuck.Status != "queued" {
		t.Errorf("stuck status = %q, want queued", gotStuck.Status)
	}

	gotFresh, err := messages.Get(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if gotFresh.Status != "sending" {
		t.Errorf("fresh status = %q, want sending", gotFresh.Status)
	}

	claimed, err = messages.ClaimQueued(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimQueued after requeue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != stuck.ID {
		t.Errorf("ClaimQueued after requeue = %+v, want [%s]", claimed, stuck.ID)
	}

	requeued, err = messages.RequeueStuck(ctx, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("RequeueStuck second call: %v", err)
	}
	if requeued != 0 {
		t.Errorf("RequeueStuck second call = %d, want 0", requeued)
	}
}
