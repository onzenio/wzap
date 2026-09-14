package postgres

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
)

// TestInstanceRepositoryWebhookRoundtrip proves the instance write paths store
// the three webhook columns: Create persists them, Update replaces them, and
// an unset URL with an empty subscription round-trips back.
func TestInstanceRepositoryWebhookRoundtrip(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repo := NewInstanceRepository(pool)

	url := "https://hooks.example.com/wzap"
	created, err := repo.Create(ctx, model.Instance{
		ID: uuid.New(), Name: "webhook", Status: "disconnected",
		WebhookURL: &url, WebhookEnabled: true,
		WebhookEvents: []string{"message", "message.status"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.WebhookURL == nil || *created.WebhookURL != url {
		t.Errorf("Create WebhookURL = %v, want %q", created.WebhookURL, url)
	}
	if !created.WebhookEnabled {
		t.Error("Create WebhookEnabled = false, want true")
	}
	if want := []string{"message", "message.status"}; !reflect.DeepEqual(created.WebhookEvents, want) {
		t.Errorf("Create WebhookEvents = %v, want %v", created.WebhookEvents, want)
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WebhookURL == nil || *got.WebhookURL != url {
		t.Errorf("Get WebhookURL = %v, want %q", got.WebhookURL, url)
	}
	if !reflect.DeepEqual(got.WebhookEvents, []string{"message", "message.status"}) {
		t.Errorf("Get WebhookEvents = %v, want the stored subscription", got.WebhookEvents)
	}

	got.WebhookURL = nil
	got.WebhookEnabled = false
	got.WebhookEvents = []string{}
	updated, err := repo.Update(ctx, *got)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.WebhookURL != nil {
		t.Errorf("Update WebhookURL = %q, want nil (unset)", *updated.WebhookURL)
	}
	if updated.WebhookEnabled {
		t.Error("Update WebhookEnabled = true, want false")
	}
	if updated.WebhookEvents == nil || len(updated.WebhookEvents) != 0 {
		t.Errorf("Update WebhookEvents = %v, want the explicit empty list", updated.WebhookEvents)
	}

	cleared, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if cleared.WebhookURL != nil {
		t.Errorf("Get WebhookURL = %q, want nil (unset)", *cleared.WebhookURL)
	}
	if cleared.WebhookEvents == nil || len(cleared.WebhookEvents) != 0 {
		t.Errorf("Get WebhookEvents = %v, want the explicit empty list", cleared.WebhookEvents)
	}
}
