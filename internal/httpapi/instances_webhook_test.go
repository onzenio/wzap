package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/instance"
	"wzap/internal/model"
)

// webhookRoundTripFake is a stateful InstanceService fake: Create and Update
// apply the webhook inputs they receive, so the tests can round-trip a
// configuration through POST/PATCH and read it back with GET.
type webhookRoundTripFake struct {
	fakeInstanceService
	stored *model.Instance
}

func newWebhookRoundTripFake() *webhookRoundTripFake {
	f := &webhookRoundTripFake{}
	f.createFn = func(_ context.Context, input instance.CreateInput) (*model.Instance, string, error) {
		stored := &model.Instance{
			ID: uuid.New(), Name: input.Name, ExternalRef: input.ExternalRef,
			Status: "disconnected", OwnerUserID: input.OwnerUserID,
		}
		if input.WebhookURL != nil {
			url := *input.WebhookURL
			stored.WebhookURL = &url
		}
		if input.WebhookEnabled != nil {
			stored.WebhookEnabled = *input.WebhookEnabled
		}
		if input.WebhookEvents != nil {
			stored.WebhookEvents = append([]string(nil), (*input.WebhookEvents)...)
		} else {
			stored.WebhookEvents = []string{"message", "receipt", "connection", "message.status"}
		}
		f.stored = stored
		return stored, "one-time-key", nil
	}
	f.getFn = func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
		if f.stored == nil || f.stored.ID != id {
			return nil, instance.ErrNotFound
		}
		return f.stored, nil
	}
	f.updateFn = func(_ context.Context, id uuid.UUID, input instance.UpdateInput) (*model.Instance, error) {
		if f.stored == nil || f.stored.ID != id {
			return nil, instance.ErrNotFound
		}
		if input.WebhookURL != nil {
			if *input.WebhookURL == "" {
				f.stored.WebhookURL = nil
			} else {
				url := *input.WebhookURL
				f.stored.WebhookURL = &url
			}
		}
		if input.WebhookEnabled != nil {
			f.stored.WebhookEnabled = *input.WebhookEnabled
		}
		if input.WebhookEvents != nil {
			f.stored.WebhookEvents = append([]string(nil), (*input.WebhookEvents)...)
		}
		return f.stored, nil
	}
	return f
}

func webhookData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	decodeJSON(t, body, &payload)
	return payload.Data
}

func TestInstancesCreateWithWebhookRoundTrip(t *testing.T) {
	svc := newWebhookRoundTripFake()
	srv := instancesServer(t, svc)

	rec := serveJSON(t, srv, http.MethodPost, "/instances",
		`{"name":"loja","webhook_url":"https://hooks.example.com/wzap","webhook_enabled":true,"webhook_events":["message","receipt"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(svc.createInputs) != 1 {
		t.Fatalf("Create calls = %d, want 1", len(svc.createInputs))
	}
	sent := svc.createInputs[0]
	if sent.WebhookURL == nil || *sent.WebhookURL != "https://hooks.example.com/wzap" {
		t.Errorf("Create webhook_url = %v, want the configured URL", sent.WebhookURL)
	}
	if sent.WebhookEnabled == nil || !*sent.WebhookEnabled {
		t.Errorf("Create webhook_enabled = %v, want an explicit true", sent.WebhookEnabled)
	}
	if sent.WebhookEvents == nil || !reflect.DeepEqual(*sent.WebhookEvents, []string{"message", "receipt"}) {
		t.Errorf("Create webhook_events = %v, want [message receipt]", sent.WebhookEvents)
	}

	data := webhookData(t, rec.Body.Bytes())
	if data["webhook_url"] != "https://hooks.example.com/wzap" {
		t.Errorf("data.webhook_url = %v, want the configured URL", data["webhook_url"])
	}
	if data["webhook_enabled"] != true {
		t.Errorf("data.webhook_enabled = %v, want true", data["webhook_enabled"])
	}
	if !reflect.DeepEqual(data["webhook_events"], []any{"message", "receipt"}) {
		t.Errorf("data.webhook_events = %v, want [message receipt]", data["webhook_events"])
	}
	id, _ := data["id"].(string)

	got := serveJSON(t, srv, http.MethodGet, "/instances/"+id, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d (body %q)", got.Code, http.StatusOK, got.Body.String())
	}
	getData := webhookData(t, got.Body.Bytes())
	if getData["webhook_url"] != "https://hooks.example.com/wzap" {
		t.Errorf("get webhook_url = %v, want the configured URL", getData["webhook_url"])
	}
	if getData["webhook_enabled"] != true {
		t.Errorf("get webhook_enabled = %v, want true", getData["webhook_enabled"])
	}
	if !reflect.DeepEqual(getData["webhook_events"], []any{"message", "receipt"}) {
		t.Errorf("get webhook_events = %v, want [message receipt]", getData["webhook_events"])
	}
}

func TestInstancesUpdateWebhookDisablesAndNarrows(t *testing.T) {
	svc := newWebhookRoundTripFake()
	srv := instancesServer(t, svc)

	created := serveJSON(t, srv, http.MethodPost, "/instances",
		`{"name":"loja","webhook_url":"https://hooks.example.com/wzap","webhook_enabled":true}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body %q)", created.Code, http.StatusCreated, created.Body.String())
	}
	id, _ := webhookData(t, created.Body.Bytes())["id"].(string)

	updated := serveJSON(t, srv, http.MethodPatch, "/instances/"+id,
		`{"webhook_enabled":false,"webhook_events":["receipt"]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body %q)", updated.Code, http.StatusOK, updated.Body.String())
	}
	if len(svc.updateInputs) != 1 {
		t.Fatalf("Update calls = %d, want 1", len(svc.updateInputs))
	}
	sent := svc.updateInputs[0]
	if sent.WebhookEnabled == nil || *sent.WebhookEnabled {
		t.Errorf("Update webhook_enabled = %v, want an explicit false", sent.WebhookEnabled)
	}
	if sent.WebhookEvents == nil || !reflect.DeepEqual(*sent.WebhookEvents, []string{"receipt"}) {
		t.Errorf("Update webhook_events = %v, want [receipt]", sent.WebhookEvents)
	}
	if sent.WebhookURL != nil {
		t.Errorf("Update webhook_url = %v, want nil (absent keeps the stored URL)", sent.WebhookURL)
	}

	data := webhookData(t, updated.Body.Bytes())
	if data["webhook_url"] != "https://hooks.example.com/wzap" {
		t.Errorf("data.webhook_url = %v, want the stored URL kept", data["webhook_url"])
	}
	if data["webhook_enabled"] != false {
		t.Errorf("data.webhook_enabled = %v, want false", data["webhook_enabled"])
	}
	if !reflect.DeepEqual(data["webhook_events"], []any{"receipt"}) {
		t.Errorf("data.webhook_events = %v, want [receipt]", data["webhook_events"])
	}
}

func TestInstancesCreateInvalidWebhookUnprocessable(t *testing.T) {
	svc := &fakeInstanceService{createFn: func(context.Context, instance.CreateInput) (*model.Instance, string, error) {
		return nil, "", instance.ErrInvalidWebhook
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/instances",
		`{"name":"loja","webhook_url":"http://example.com/hook"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unprocessable_entity" {
		t.Errorf("error code = %q, want %q", code, "unprocessable_entity")
	}
}

// A 422 on update keeps the previous configuration: the failed write stores
// nothing, so reading the instance back shows the old webhook.
func TestInstancesUpdateInvalidWebhookKeepsPrevious(t *testing.T) {
	svc := newWebhookRoundTripFake()
	srv := instancesServer(t, svc)

	created := serveJSON(t, srv, http.MethodPost, "/instances",
		`{"name":"loja","webhook_url":"https://hooks.example.com/wzap","webhook_enabled":true,"webhook_events":["message"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body %q)", created.Code, http.StatusCreated, created.Body.String())
	}
	id, _ := webhookData(t, created.Body.Bytes())["id"].(string)

	svc.updateFn = func(_ context.Context, _ uuid.UUID, _ instance.UpdateInput) (*model.Instance, error) {
		return nil, instance.ErrInvalidWebhook
	}
	bad := serveJSON(t, srv, http.MethodPatch, "/instances/"+id,
		`{"webhook_url":"http://example.com/hook"}`)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("update status = %d, want %d (body %q)", bad.Code, http.StatusUnprocessableEntity, bad.Body.String())
	}
	if code := errorCode(t, bad.Body.Bytes()); code != "unprocessable_entity" {
		t.Errorf("error code = %q, want %q", code, "unprocessable_entity")
	}

	got := serveJSON(t, srv, http.MethodGet, "/instances/"+id, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d (body %q)", got.Code, http.StatusOK, got.Body.String())
	}
	data := webhookData(t, got.Body.Bytes())
	if data["webhook_url"] != "https://hooks.example.com/wzap" {
		t.Errorf("get webhook_url = %v, want the previous URL kept", data["webhook_url"])
	}
	if !reflect.DeepEqual(data["webhook_events"], []any{"message"}) {
		t.Errorf("get webhook_events = %v, want the previous [message] kept", data["webhook_events"])
	}
}
