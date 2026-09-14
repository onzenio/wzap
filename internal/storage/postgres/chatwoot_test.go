package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/storage"
	"wzap/internal/storage/postgres/postgrestest"
)

func TestChatwootConfigPutAndGet(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.NewPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}

	instances := NewInstanceRepository(pool)
	instance, err := instances.Create(ctx, model.Instance{
		ID:     uuid.New(),
		Name:   "chatwoot-cfg",
		Status: "disconnected",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	cfgRepo, _ := NewChatwootRepositories(pool)
	want := model.ChatwootConfig{
		InstanceID:          instance.ID,
		Enabled:             true,
		URL:                 "https://chatwoot.example.com",
		AccountID:           "42",
		Token:               "secret-token",
		NameInbox:           "wzap-inbox",
		SignMsg:             true,
		SignDelimiter:       "\n",
		ReopenConversation:  true,
		ConversationPending: false,
		MergeBrazilContacts: true,
		ImportContacts:      true,
		ImportMessages:      false,
		DaysLimit:           30,
		AutoCreate:          true,
		Organization:        "acme",
		Logo:                "https://example.com/logo.png",
		IgnoreJIDs:          []string{"123@s.whatsapp.net"},
	}

	stored, err := cfgRepo.Put(ctx, want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if stored.InstanceID != want.InstanceID {
		t.Errorf("Put InstanceID = %s, want %s", stored.InstanceID, want.InstanceID)
	}

	got, err := cfgRepo.Get(ctx, instance.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InstanceID != want.InstanceID {
		t.Errorf("Get InstanceID = %s, want %s", got.InstanceID, want.InstanceID)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Get Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
	if got.URL != want.URL {
		t.Errorf("Get URL = %q, want %q", got.URL, want.URL)
	}
	if got.AccountID != want.AccountID {
		t.Errorf("Get AccountID = %q, want %q", got.AccountID, want.AccountID)
	}
	if got.Token != want.Token {
		t.Errorf("Get Token = %q, want %q", got.Token, want.Token)
	}
	if got.NameInbox != want.NameInbox {
		t.Errorf("Get NameInbox = %q, want %q", got.NameInbox, want.NameInbox)
	}
	if got.SignMsg != want.SignMsg {
		t.Errorf("Get SignMsg = %v, want %v", got.SignMsg, want.SignMsg)
	}
	if got.SignDelimiter != want.SignDelimiter {
		t.Errorf("Get SignDelimiter = %q, want %q", got.SignDelimiter, want.SignDelimiter)
	}
	if got.ReopenConversation != want.ReopenConversation {
		t.Errorf("Get ReopenConversation = %v, want %v", got.ReopenConversation, want.ReopenConversation)
	}
	if got.ConversationPending != want.ConversationPending {
		t.Errorf("Get ConversationPending = %v, want %v", got.ConversationPending, want.ConversationPending)
	}
	if got.MergeBrazilContacts != want.MergeBrazilContacts {
		t.Errorf("Get MergeBrazilContacts = %v, want %v", got.MergeBrazilContacts, want.MergeBrazilContacts)
	}
	if got.ImportContacts != want.ImportContacts {
		t.Errorf("Get ImportContacts = %v, want %v", got.ImportContacts, want.ImportContacts)
	}
	if got.ImportMessages != want.ImportMessages {
		t.Errorf("Get ImportMessages = %v, want %v", got.ImportMessages, want.ImportMessages)
	}
	if got.DaysLimit != want.DaysLimit {
		t.Errorf("Get DaysLimit = %d, want %d", got.DaysLimit, want.DaysLimit)
	}
	if got.AutoCreate != want.AutoCreate {
		t.Errorf("Get AutoCreate = %v, want %v", got.AutoCreate, want.AutoCreate)
	}
	if got.Organization != want.Organization {
		t.Errorf("Get Organization = %q, want %q", got.Organization, want.Organization)
	}
	if got.Logo != want.Logo {
		t.Errorf("Get Logo = %q, want %q", got.Logo, want.Logo)
	}
	if len(got.IgnoreJIDs) != 1 || got.IgnoreJIDs[0] != want.IgnoreJIDs[0] {
		t.Errorf("Get IgnoreJIDs = %v, want %v", got.IgnoreJIDs, want.IgnoreJIDs)
	}
	if got.CreatedAt.IsZero() {
		t.Error("Get CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("Get UpdatedAt is zero")
	}

	if _, err := cfgRepo.Get(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Get(unknown) err = %v, want %v", err, storage.ErrNotFound)
	}
}

func TestChatwootConfigPutNilAndEmptyIgnoreJIDs(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.NewPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}

	instances := NewInstanceRepository(pool)
	cfgRepo, _ := NewChatwootRepositories(pool)

	for _, tc := range []struct {
		name       string
		ignoreJIDs []string
	}{
		{name: "nil", ignoreJIDs: nil},
		{name: "empty", ignoreJIDs: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := instances.Create(ctx, model.Instance{
				ID:     uuid.New(),
				Name:   "chatwoot-ignore-" + tc.name,
				Status: "disconnected",
			})
			if err != nil {
				t.Fatalf("create instance: %v", err)
			}

			stored, err := cfgRepo.Put(ctx, model.ChatwootConfig{
				InstanceID: instance.ID,
				IgnoreJIDs: tc.ignoreJIDs,
			})
			if err != nil {
				t.Fatalf("Put(IgnoreJIDs=%v): %v", tc.ignoreJIDs, err)
			}
			if len(stored.IgnoreJIDs) != 0 {
				t.Errorf("Put IgnoreJIDs = %v, want empty", stored.IgnoreJIDs)
			}

			got, err := cfgRepo.Get(ctx, instance.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if len(got.IgnoreJIDs) != 0 {
				t.Errorf("Get IgnoreJIDs = %v, want empty", got.IgnoreJIDs)
			}
		})
	}
}

func TestChatwootMessagePutGetDeleteByInstance(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.NewPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}

	instances := NewInstanceRepository(pool)
	instance, err := instances.Create(ctx, model.Instance{
		ID:     uuid.New(),
		Name:   "chatwoot-msg",
		Status: "disconnected",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	_, msgRepo := NewChatwootRepositories(pool)
	want := model.ChatwootMessage{
		InstanceID:        instance.ID,
		WAKey:             "WAID:ABC123",
		ChatwootMessageID: 101,
		ConversationID:    202,
		InboxID:           303,
		ContactSourceID:   "source-1",
		IsRead:            false,
	}

	if _, err := msgRepo.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := msgRepo.GetByWAKey(ctx, instance.ID, want.WAKey)
	if err != nil {
		t.Fatalf("GetByWAKey: %v", err)
	}
	if got.InstanceID != want.InstanceID {
		t.Errorf("GetByWAKey InstanceID = %s, want %s", got.InstanceID, want.InstanceID)
	}
	if got.WAKey != want.WAKey {
		t.Errorf("GetByWAKey WAKey = %q, want %q", got.WAKey, want.WAKey)
	}
	if got.ChatwootMessageID != want.ChatwootMessageID {
		t.Errorf("GetByWAKey ChatwootMessageID = %d, want %d", got.ChatwootMessageID, want.ChatwootMessageID)
	}
	if got.ConversationID != want.ConversationID {
		t.Errorf("GetByWAKey ConversationID = %d, want %d", got.ConversationID, want.ConversationID)
	}
	if got.InboxID != want.InboxID {
		t.Errorf("GetByWAKey InboxID = %d, want %d", got.InboxID, want.InboxID)
	}
	if got.ContactSourceID != want.ContactSourceID {
		t.Errorf("GetByWAKey ContactSourceID = %q, want %q", got.ContactSourceID, want.ContactSourceID)
	}
	if got.IsRead != want.IsRead {
		t.Errorf("GetByWAKey IsRead = %v, want %v", got.IsRead, want.IsRead)
	}
	if got.CreatedAt.IsZero() {
		t.Error("GetByWAKey CreatedAt is zero")
	}

	if _, err := msgRepo.GetByWAKey(ctx, instance.ID, "WAID:missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByWAKey(missing) err = %v, want %v", err, storage.ErrNotFound)
	}

	removed, err := msgRepo.DeleteByInstance(ctx, instance.ID)
	if err != nil {
		t.Fatalf("DeleteByInstance: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeleteByInstance = %d, want 1", removed)
	}
	if _, err := msgRepo.GetByWAKey(ctx, instance.ID, want.WAKey); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByWAKey(after delete) err = %v, want %v", err, storage.ErrNotFound)
	}
}

// TestLatestByConversationTiebreaksDeterministically pins migration 00004:
// Migrate applies the covering index on a clean DB, and ties on created_at
// resolve to the larger chatwoot_message_id instead of an arbitrary row.
func TestLatestByConversationTiebreaksDeterministically(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.NewPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}

	var indexRegclass *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('chatwoot_messages_conversation_idx')::text`).Scan(&indexRegclass); err != nil {
		t.Fatalf("lookup covering index: %v", err)
	}
	if indexRegclass == nil {
		t.Fatal("covering index chatwoot_messages_conversation_idx missing after Migrate")
	}

	instances := NewInstanceRepository(pool)
	instance, err := instances.Create(ctx, model.Instance{
		ID:     uuid.New(),
		Name:   "chatwoot-latest",
		Status: "disconnected",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	// Two rows sharing the exact same created_at: Put stamps now() per row,
	// so the tie is seeded with explicit SQL.
	stamp := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := pool.Exec(ctx,
		`INSERT INTO chatwoot_messages (instance_id, wa_key, chatwoot_message_id, conversation_id, inbox_id, contact_source_id, created_at)
		 VALUES ($1, 'WA-TIE-1', 11, 55, 7, 'source', $2), ($1, 'WA-TIE-2', 22, 55, 7, 'source', $2)`,
		instance.ID, stamp); err != nil {
		t.Fatalf("seed tied rows: %v", err)
	}

	_, msgRepo := NewChatwootRepositories(pool)
	got, err := msgRepo.LatestByConversation(ctx, instance.ID, 55)
	if err != nil {
		t.Fatalf("LatestByConversation: %v", err)
	}
	if got.ChatwootMessageID != 22 {
		t.Errorf("LatestByConversation ChatwootMessageID = %d, want 22 (deterministic tiebreak)", got.ChatwootMessageID)
	}
	if got.WAKey != "WA-TIE-2" {
		t.Errorf("LatestByConversation WAKey = %q, want WA-TIE-2", got.WAKey)
	}

	if _, err := msgRepo.LatestByConversation(ctx, instance.ID, 999); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("LatestByConversation(unknown) err = %v, want %v", err, storage.ErrNotFound)
	}
}
