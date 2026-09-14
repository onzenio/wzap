package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/instance"
	"wzap/internal/message"
	"wzap/internal/model"
)

// fakeNumberResolver records the number checks the handlers made and answers
// with the configured outcome, defaulting to ErrNumberNotFound.
type fakeNumberResolver struct {
	resolveFn func(ctx context.Context, instanceID uuid.UUID, phone string) (string, error)

	instanceIDs []uuid.UUID
	phones      []string
}

// Resolve records the call and returns the configured result.
func (f *fakeNumberResolver) Resolve(ctx context.Context, instanceID uuid.UUID, phone string) (string, error) {
	f.instanceIDs = append(f.instanceIDs, instanceID)
	f.phones = append(f.phones, phone)
	if f.resolveFn != nil {
		return f.resolveFn(ctx, instanceID, phone)
	}
	return "", message.ErrNumberNotFound
}

// numbersServer builds the server under test with the given instance and
// number services.
func numbersServer(t *testing.T, instances InstanceService, numbers NumberResolver) *http.Server {
	t.Helper()
	if instances == nil {
		instances = &fakeInstanceService{}
	}
	if numbers == nil {
		numbers = &fakeNumberResolver{}
	}
	return New(config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken}, discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    instances,
			Numbers:      numbers,
		})
}

// existingInstanceService returns an instance service that answers Get with a
// stored instance.
func existingInstanceService() *fakeInstanceService {
	return &fakeInstanceService{getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
		return &model.Instance{ID: id, Name: "loja", Status: "connected"}, nil
	}}
}

// numberCheckPayload is the decoded data of a number check response.
type numberCheckPayload struct {
	Exists     bool   `json:"exists"`
	JID        string `json:"jid"`
	Normalized string `json:"normalized"`
}

func TestNumbersCheckFound(t *testing.T) {
	id := uuid.New()
	resolver := &fakeNumberResolver{resolveFn: func(_ context.Context, gotID uuid.UUID, phone string) (string, error) {
		if gotID != id {
			t.Errorf("Resolve instance = %s, want %s", gotID, id)
		}
		if phone != "+55 (47) 98835-9190" {
			t.Errorf("Resolve phone = %q, want the raw request phone", phone)
		}
		return "5547988359190@s.whatsapp.net", nil
	}}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/"+id.String()+"/numbers/check", `{"phone":"+55 (47) 98835-9190"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data numberCheckPayload `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if !payload.Data.Exists {
		t.Error("data.exists = false, want true")
	}
	if payload.Data.JID != "5547988359190@s.whatsapp.net" {
		t.Errorf("data.jid = %q, want the resolved JID", payload.Data.JID)
	}
	if payload.Data.Normalized != "5547988359190" {
		t.Errorf("data.normalized = %q, want the digits of the request phone", payload.Data.Normalized)
	}
}

func TestNumbersCheckNotFound(t *testing.T) {
	id := uuid.New()
	resolver := &fakeNumberResolver{resolveFn: func(context.Context, uuid.UUID, string) (string, error) {
		return "", message.ErrNumberNotFound
	}}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/"+id.String()+"/numbers/check", `{"phone":"5547999999999"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data numberCheckPayload `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Exists {
		t.Error("data.exists = true, want false")
	}
	if payload.Data.JID != "" {
		t.Errorf("data.jid = %q, want empty", payload.Data.JID)
	}
	if payload.Data.Normalized != "5547999999999" {
		t.Errorf("data.normalized = %q, want the digits of the request phone", payload.Data.Normalized)
	}
}

func TestNumbersCheckInstanceNotFound(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{getFn: func(context.Context, uuid.UUID) (*model.Instance, error) {
		return nil, instance.ErrNotFound
	}}
	resolver := &fakeNumberResolver{}

	rec := serveJSON(t, numbersServer(t, svc, resolver), http.MethodPost,
		"/api/v1/instances/"+id.String()+"/numbers/check", `{"phone":"5547988359190"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
	if len(resolver.phones) != 0 {
		t.Errorf("Resolve calls = %v, want none for an unknown instance", resolver.phones)
	}
}

func TestNumbersCheckResolverUnavailable(t *testing.T) {
	id := uuid.New()
	resolver := &fakeNumberResolver{resolveFn: func(context.Context, uuid.UUID, string) (string, error) {
		return "", message.ErrResolverUnavailable
	}}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/"+id.String()+"/numbers/check", `{"phone":"5547988359190"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unavailable" {
		t.Errorf("error code = %q, want %q", code, "unavailable")
	}
}

func TestNumbersCheckRejectsInvalidBody(t *testing.T) {
	resolver := &fakeNumberResolver{}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/"+uuid.NewString()+"/numbers/check", `{"phone":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "invalid_request" {
		t.Errorf("error code = %q, want %q", code, "invalid_request")
	}
	if len(resolver.phones) != 0 {
		t.Errorf("Resolve calls = %v, want none on a malformed body", resolver.phones)
	}
}

func TestNumbersCheckRejectsEmptyPhone(t *testing.T) {
	svc := &fakeInstanceService{}
	resolver := &fakeNumberResolver{}

	rec := serveJSON(t, numbersServer(t, svc, resolver), http.MethodPost,
		"/api/v1/instances/"+uuid.NewString()+"/numbers/check", `{"phone":"  "}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "invalid_request" {
		t.Errorf("error code = %q, want %q", code, "invalid_request")
	}
	if len(svc.getIDs) != 0 {
		t.Errorf("Get calls = %v, want none for an empty phone", svc.getIDs)
	}
}

func TestNumbersCheckRejectsMalformedInstanceID(t *testing.T) {
	resolver := &fakeNumberResolver{}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/not-a-uuid/numbers/check", `{"phone":"5547988359190"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
	if len(resolver.phones) != 0 {
		t.Errorf("Resolve calls = %v, want none for a malformed id", resolver.phones)
	}
}

func TestNumbersCheckInternalError(t *testing.T) {
	id := uuid.New()
	resolver := &fakeNumberResolver{resolveFn: func(context.Context, uuid.UUID, string) (string, error) {
		return "", context.DeadlineExceeded
	}}

	rec := serveJSON(t, numbersServer(t, existingInstanceService(), resolver), http.MethodPost,
		"/api/v1/instances/"+id.String()+"/numbers/check", `{"phone":"5547988359190"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "internal_error" {
		t.Errorf("error code = %q, want %q", code, "internal_error")
	}
	if strings.Contains(rec.Body.String(), "deadline") {
		t.Errorf("body leaks the internal error: %q", rec.Body.String())
	}
}
