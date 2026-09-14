package instance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session/sessiontest"
)

func boolptr(b bool) *bool { return &b }

func webhookEventsPtr(events ...string) *[]string {
	if events == nil {
		empty := []string{}
		return &empty
	}
	return &events
}

func newWebhookService(repo *fakeRepo, owner model.User) *Service {
	return NewService(repo, sessiontest.New(nil), &fakeMedia{}, newFakeUserRepo(owner), newFakeKeyRepo())
}

func TestServiceCreateWebhookDefaults(t *testing.T) {
	repo := newFakeRepo()
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
	svc := newWebhookService(repo, owner)

	created, _, err := svc.Create(context.Background(), CreateInput{Name: "loja", OwnerUserID: &owner.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.WebhookURL != nil {
		t.Errorf("WebhookURL = %q, want nil (unset)", *created.WebhookURL)
	}
	if created.WebhookEnabled {
		t.Error("WebhookEnabled = true, want false by default")
	}
	wantEvents := []string{"message", "receipt", "connection", "message.status"}
	if !reflect.DeepEqual(created.WebhookEvents, wantEvents) {
		t.Errorf("WebhookEvents = %v, want %v", created.WebhookEvents, wantEvents)
	}
}

func TestServiceCreateWithWebhook(t *testing.T) {
	repo := newFakeRepo()
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
	svc := newWebhookService(repo, owner)

	created, _, err := svc.Create(context.Background(), CreateInput{
		Name: "loja", OwnerUserID: &owner.ID,
		WebhookURL:     strptr("https://hooks.example.com/wzap"),
		WebhookEnabled: boolptr(true),
		// Reversed on purpose: storage follows the canonical order.
		WebhookEvents: webhookEventsPtr("message.status", "message"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if created.WebhookURL == nil || *created.WebhookURL != "https://hooks.example.com/wzap" {
		t.Errorf("WebhookURL = %v, want the configured URL", created.WebhookURL)
	}
	if !created.WebhookEnabled {
		t.Error("WebhookEnabled = false, want true")
	}
	if want := []string{"message", "message.status"}; !reflect.DeepEqual(created.WebhookEvents, want) {
		t.Errorf("WebhookEvents = %v, want %v", created.WebhookEvents, want)
	}
}

// Ruling R22: an empty URL stays valid even with enabled set; it stores unset
// and delivers nothing.
func TestServiceCreateEnabledWithoutURL(t *testing.T) {
	repo := newFakeRepo()
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
	svc := newWebhookService(repo, owner)

	created, _, err := svc.Create(context.Background(), CreateInput{
		Name: "loja", OwnerUserID: &owner.ID, WebhookEnabled: boolptr(true),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.WebhookURL != nil {
		t.Errorf("WebhookURL = %q, want nil (unset)", *created.WebhookURL)
	}
	if !created.WebhookEnabled {
		t.Error("WebhookEnabled = false, want the explicit true stored")
	}
}

func TestServiceCreateInvalidWebhookPersistsNothing(t *testing.T) {
	tests := []struct {
		name  string
		input CreateInput
	}{
		{name: "http outside loopback", input: CreateInput{WebhookURL: strptr("http://example.com/hook")}},
		{name: "non-http scheme", input: CreateInput{WebhookURL: strptr("ftp://example.com/hook")}},
		{name: "unknown event type", input: CreateInput{WebhookEvents: webhookEventsPtr("bogus")}},
		{name: "uppercase type not normalized", input: CreateInput{WebhookEvents: webhookEventsPtr("Message")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
			svc := newWebhookService(repo, owner)
			tt.input.Name = "loja"
			tt.input.OwnerUserID = &owner.ID

			_, _, err := svc.Create(context.Background(), tt.input)
			if !errors.Is(err, ErrInvalidWebhook) {
				t.Fatalf("Create error = %v, want ErrInvalidWebhook", err)
			}
			if len(repo.createCalls) != 0 {
				t.Errorf("repo Create calls = %d, want none: a 422 never persists", len(repo.createCalls))
			}
		})
	}
}

func TestServiceUpdateWebhookPartial(t *testing.T) {
	url := "https://hooks.example.com/wzap"
	id := uuid.New()
	stored := model.Instance{
		ID: id, Name: "loja", Status: "disconnected",
		WebhookURL: &url, WebhookEnabled: true,
		WebhookEvents: []string{"message", "receipt", "connection", "message.status"},
	}

	t.Run("absent fields keep stored config", func(t *testing.T) {
		repo := newFakeRepo(stored)
		svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)

		updated, err := svc.Update(context.Background(), id, UpdateInput{Name: strptr("novo")})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.WebhookURL == nil || *updated.WebhookURL != url {
			t.Errorf("WebhookURL = %v, want the stored %q", updated.WebhookURL, url)
		}
		if !updated.WebhookEnabled {
			t.Error("WebhookEnabled = false, want the stored true")
		}
		if !reflect.DeepEqual(updated.WebhookEvents, stored.WebhookEvents) {
			t.Errorf("WebhookEvents = %v, want the stored %v", updated.WebhookEvents, stored.WebhookEvents)
		}
	})

	t.Run("explicit fields replace stored config", func(t *testing.T) {
		repo := newFakeRepo(stored)
		svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)

		updated, err := svc.Update(context.Background(), id, UpdateInput{
			WebhookURL:     strptr("http://127.0.0.1:8080/hook"),
			WebhookEnabled: boolptr(false),
			WebhookEvents:  webhookEventsPtr("receipt"),
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.WebhookURL == nil || *updated.WebhookURL != "http://127.0.0.1:8080/hook" {
			t.Errorf("WebhookURL = %v, want the loopback replacement", updated.WebhookURL)
		}
		if updated.WebhookEnabled {
			t.Error("WebhookEnabled = true, want the explicit false")
		}
		if want := []string{"receipt"}; !reflect.DeepEqual(updated.WebhookEvents, want) {
			t.Errorf("WebhookEvents = %v, want %v", updated.WebhookEvents, want)
		}
	})

	t.Run("explicit empty url unsets and empty events clear", func(t *testing.T) {
		repo := newFakeRepo(stored)
		svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)

		updated, err := svc.Update(context.Background(), id, UpdateInput{
			WebhookURL: strptr(""), WebhookEvents: webhookEventsPtr(),
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.WebhookURL != nil {
			t.Errorf("WebhookURL = %q, want nil (unset)", *updated.WebhookURL)
		}
		if updated.WebhookEvents == nil || len(updated.WebhookEvents) != 0 {
			t.Errorf("WebhookEvents = %v, want the explicit empty list", updated.WebhookEvents)
		}
		if !updated.WebhookEnabled {
			t.Error("WebhookEnabled = false, want the stored true kept")
		}
	})
}

func TestServiceUpdateInvalidWebhookKeepsPrevious(t *testing.T) {
	url := "https://hooks.example.com/wzap"
	tests := []struct {
		name  string
		input UpdateInput
	}{
		{name: "http outside loopback", input: UpdateInput{WebhookURL: strptr("http://10.0.0.5/hook")}},
		{name: "non-http scheme", input: UpdateInput{WebhookURL: strptr("ws://example.com/hook")}},
		{name: "unknown event type", input: UpdateInput{WebhookEvents: webhookEventsPtr("message", "bogus")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := uuid.New()
			stored := model.Instance{
				ID: id, Name: "loja", Status: "disconnected",
				WebhookURL: &url, WebhookEnabled: true,
				WebhookEvents: []string{"message", "receipt", "connection", "message.status"},
			}
			repo := newFakeRepo(stored)
			svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, nil, nil)

			_, err := svc.Update(context.Background(), id, tt.input)
			if !errors.Is(err, ErrInvalidWebhook) {
				t.Fatalf("Update error = %v, want ErrInvalidWebhook", err)
			}
			if len(repo.updateCalls) != 0 {
				t.Errorf("repo Update calls = %d, want none: a 422 keeps the previous config", len(repo.updateCalls))
			}
			kept, getErr := repo.Get(context.Background(), id)
			if getErr != nil {
				t.Fatalf("Get after failed update: %v", getErr)
			}
			if !reflect.DeepEqual(*kept, stored) {
				t.Errorf("stored = %+v, want the unchanged %+v", *kept, stored)
			}
		})
	}
}
