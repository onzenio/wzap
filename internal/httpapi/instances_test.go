package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/instance"
	"wzap/internal/model"
)

// fakeInstanceService is an in-memory InstanceService: the function fields
// configure each outcome and the recorded fields expose the calls the handlers
// made.
type fakeInstanceService struct {
	createFn     func(ctx context.Context, input instance.CreateInput) (*model.Instance, error)
	getFn        func(ctx context.Context, id uuid.UUID) (*model.Instance, error)
	listFn       func(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error)
	updateFn     func(ctx context.Context, id uuid.UUID, input instance.UpdateInput) (*model.Instance, error)
	deleteFn     func(ctx context.Context, id uuid.UUID) error
	disconnectFn func(ctx context.Context, id uuid.UUID) error
	connectFn    func(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error)
	qrFn         func(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error)

	createInputs  []instance.CreateInput
	updateInputs  []instance.UpdateInput
	getIDs        []uuid.UUID
	deleteIDs     []uuid.UUID
	disconnectIDs []uuid.UUID
	connectIDs    []uuid.UUID
	qrIDs         []uuid.UUID
	listLimit     int
	listCursor    string
}

// Create records the input and returns the configured instance, defaulting to a
// fresh disconnected instance.
func (f *fakeInstanceService) Create(ctx context.Context, input instance.CreateInput) (*model.Instance, error) {
	f.createInputs = append(f.createInputs, input)
	if f.createFn != nil {
		return f.createFn(ctx, input)
	}
	return &model.Instance{ID: uuid.New(), Name: input.Name, ExternalRef: input.ExternalRef, Status: "disconnected"}, nil
}

// Get records the id and returns the configured instance, defaulting to
// instance.ErrNotFound.
func (f *fakeInstanceService) Get(ctx context.Context, id uuid.UUID) (*model.Instance, error) {
	f.getIDs = append(f.getIDs, id)
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return nil, instance.ErrNotFound
}

// List records the pagination and returns the configured page, defaulting to an
// empty one.
func (f *fakeInstanceService) List(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error) {
	f.listLimit = limit
	f.listCursor = cursor
	if f.listFn != nil {
		return f.listFn(ctx, limit, cursor)
	}
	return nil, "", nil
}

// Update records the input and returns the configured instance.
func (f *fakeInstanceService) Update(ctx context.Context, id uuid.UUID, input instance.UpdateInput) (*model.Instance, error) {
	f.updateInputs = append(f.updateInputs, input)
	if f.updateFn != nil {
		return f.updateFn(ctx, id, input)
	}
	return &model.Instance{ID: id, Name: "loja", Status: "disconnected"}, nil
}

// Delete records the id and returns the configured error.
func (f *fakeInstanceService) Delete(ctx context.Context, id uuid.UUID) error {
	f.deleteIDs = append(f.deleteIDs, id)
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

// Disconnect records the id and returns the configured error.
func (f *fakeInstanceService) Disconnect(ctx context.Context, id uuid.UUID) error {
	f.disconnectIDs = append(f.disconnectIDs, id)
	if f.disconnectFn != nil {
		return f.disconnectFn(ctx, id)
	}
	return nil
}

// Connect records the id and returns the configured result, defaulting to a
// fresh pairing result.
func (f *fakeInstanceService) Connect(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error) {
	f.connectIDs = append(f.connectIDs, id)
	if f.connectFn != nil {
		return f.connectFn(ctx, id)
	}
	expiresAt := time.Now().Add(time.Minute)
	return instance.ConnectResult{Status: "pairing", QRCode: "qr-code", QRExpiresAt: &expiresAt}, nil
}

// QR records the id and returns the configured result, defaulting to the same
// pairing result as Connect.
func (f *fakeInstanceService) QR(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error) {
	f.qrIDs = append(f.qrIDs, id)
	if f.qrFn != nil {
		return f.qrFn(ctx, id)
	}
	expiresAt := time.Now().Add(time.Minute)
	return instance.ConnectResult{Status: "pairing", QRCode: "qr-code", QRExpiresAt: &expiresAt}, nil
}

// instancesServer builds the server under test with svc as the instance service.
func instancesServer(t *testing.T, svc InstanceService) *http.Server {
	t.Helper()
	if svc == nil {
		svc = &fakeInstanceService{}
	}
	return New(config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken}, discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    svc,
		})
}

// serveJSON sends an authenticated request with an optional body through the
// server handler.
func serveJSON(t *testing.T, srv *http.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("apikey", testToken)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestInstancesCreate(t *testing.T) {
	created := &model.Instance{ID: uuid.New(), Name: "loja", ExternalRef: "crm-1", Status: "disconnected"}
	svc := &fakeInstanceService{createFn: func(_ context.Context, input instance.CreateInput) (*model.Instance, error) {
		if input.Name != "loja" || input.ExternalRef != "crm-1" {
			t.Errorf("Create input = %+v, want name loja and external ref crm-1", input)
		}
		return created, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/api/v1/instances",
		`{"name":"loja","external_ref":"crm-1"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	var payload struct {
		Data instanceResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.ID != created.ID.String() {
		t.Errorf("data.id = %q, want %q", payload.Data.ID, created.ID)
	}
	if payload.Data.Status != "disconnected" {
		t.Errorf("data.status = %q, want %q", payload.Data.Status, "disconnected")
	}
	if payload.Data.Name != "loja" || payload.Data.ExternalRef != "crm-1" {
		t.Errorf("data = %+v, want name loja and external ref crm-1", payload.Data)
	}
}

func TestInstancesCreateDuplicateExternalRef(t *testing.T) {
	svc := &fakeInstanceService{createFn: func(context.Context, instance.CreateInput) (*model.Instance, error) {
		return nil, instance.ErrExternalRefTaken
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/api/v1/instances",
		`{"name":"loja","external_ref":"crm-1"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
		t.Errorf("error code = %q, want %q", code, "conflict")
	}
}

func TestInstancesCreateRejectsInvalidBody(t *testing.T) {
	svc := &fakeInstanceService{}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/api/v1/instances", `{"name":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "invalid_request" {
		t.Errorf("error code = %q, want %q", code, "invalid_request")
	}
	if len(svc.createInputs) != 0 {
		t.Errorf("Create calls = %v, want none on a malformed body", svc.createInputs)
	}
}

func TestInstancesCreateRejectsOversizedBody(t *testing.T) {
	svc := &fakeInstanceService{}
	oversized := `{"name":"` + strings.Repeat("a", maxJSONBodyBytes+1) + `"}`

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPost, "/api/v1/instances", oversized)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "request_too_large" {
		t.Errorf("error code = %q, want %q", code, "request_too_large")
	}
	if len(svc.createInputs) != 0 {
		t.Errorf("Create calls = %v, want none on an oversized body", svc.createInputs)
	}
}

func TestInstancesList(t *testing.T) {
	first := model.Instance{
		ID: uuid.New(), Name: "a", ExternalRef: "ref-a", Status: "disconnected",
		WhatsAppJID: "5511@wa", CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	second := model.Instance{
		ID: uuid.New(), Name: "b", ExternalRef: "ref-b", Status: "connected",
		WhatsAppJID: "5522@wa", CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	svc := &fakeInstanceService{listFn: func(context.Context, int, string) ([]model.Instance, string, error) {
		return []model.Instance{first, second}, "cursor-1", nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data instanceListResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if len(payload.Data.Items) != 2 {
		t.Fatalf("data.items length = %d, want 2", len(payload.Data.Items))
	}
	if payload.Data.Items[0].ID != first.ID.String() {
		t.Errorf("data.items[0].id = %q, want %q", payload.Data.Items[0].ID, first.ID)
	}
	if payload.Data.Items[1].WhatsAppJID != second.WhatsAppJID {
		t.Errorf("data.items[1].whatsapp_jid = %q, want %q", payload.Data.Items[1].WhatsAppJID, second.WhatsAppJID)
	}
	if payload.Data.NextCursor != "cursor-1" {
		t.Errorf("data.next_cursor = %q, want %q", payload.Data.NextCursor, "cursor-1")
	}
	if svc.listLimit != defaultInstancesLimit {
		t.Errorf("List limit = %d, want the default %d", svc.listLimit, defaultInstancesLimit)
	}
	if svc.listCursor != "" {
		t.Errorf("List cursor = %q, want empty", svc.listCursor)
	}
}

func TestInstancesListPassesPagination(t *testing.T) {
	svc := &fakeInstanceService{}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances?limit=7&cursor=abc", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data instanceListResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Items == nil {
		t.Error("data.items = null, want an empty array")
	}
	if svc.listLimit != 7 || svc.listCursor != "abc" {
		t.Errorf("List(%d, %q), want (7, abc)", svc.listLimit, svc.listCursor)
	}
}

func TestInstancesListLimit(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "default", query: "", want: defaultInstancesLimit},
		{name: "explicit", query: "?limit=7", want: 7},
		{name: "capped", query: "?limit=1000", want: maxInstancesLimit},
		{name: "malformed falls back to default", query: "?limit=abc", want: defaultInstancesLimit},
		{name: "non positive falls back to default", query: "?limit=0", want: defaultInstancesLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeInstanceService{}

			rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances"+tt.query, "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if svc.listLimit != tt.want {
				t.Errorf("List limit = %d, want %d", svc.listLimit, tt.want)
			}
		})
	}
}

func TestInstancesListRejectsInvalidCursor(t *testing.T) {
	svc := &fakeInstanceService{listFn: func(context.Context, int, string) ([]model.Instance, string, error) {
		return nil, "", instance.ErrInvalidCursor
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances?cursor=not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "invalid_request" {
		t.Errorf("error code = %q, want %q", code, "invalid_request")
	}
}

func TestInstancesGet(t *testing.T) {
	want := &model.Instance{ID: uuid.New(), Name: "loja", ExternalRef: "crm-1", Status: "connected"}
	svc := &fakeInstanceService{getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
		if id != want.ID {
			t.Errorf("Get id = %s, want %s", id, want.ID)
		}
		return want, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances/"+want.ID.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data instanceResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.ID != want.ID.String() || payload.Data.Status != "connected" {
		t.Errorf("data = %+v, want instance %s connected", payload.Data, want.ID)
	}
}

func TestInstancesGetNotFound(t *testing.T) {
	svc := &fakeInstanceService{getFn: func(context.Context, uuid.UUID) (*model.Instance, error) {
		return nil, instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances/"+uuid.NewString(), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesGetRejectsMalformedID(t *testing.T) {
	svc := &fakeInstanceService{}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances/not-a-uuid", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
	if len(svc.getIDs) != 0 {
		t.Errorf("Get calls = %v, want none for a malformed id", svc.getIDs)
	}
}

func TestInstancesUpdate(t *testing.T) {
	id := uuid.New()
	updated := &model.Instance{ID: id, Name: "novo", ExternalRef: "ref-1", Status: "connected"}
	svc := &fakeInstanceService{updateFn: func(_ context.Context, gotID uuid.UUID, input instance.UpdateInput) (*model.Instance, error) {
		if gotID != id {
			t.Errorf("Update id = %s, want %s", gotID, id)
		}
		if input.Name == nil || *input.Name != "novo" {
			t.Errorf("Update name = %v, want novo", input.Name)
		}
		if input.ExternalRef != nil {
			t.Errorf("Update external ref = %q, want nil (preserved)", *input.ExternalRef)
		}
		return updated, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPatch, "/api/v1/instances/"+id.String(), `{"name":"novo"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload struct {
		Data instanceResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &payload)
	if payload.Data.Name != "novo" || payload.Data.ExternalRef != "ref-1" {
		t.Errorf("data = %+v, want name novo and external ref ref-1", payload.Data)
	}
}

func TestInstancesUpdateClearsExternalRef(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{updateFn: func(_ context.Context, _ uuid.UUID, input instance.UpdateInput) (*model.Instance, error) {
		if input.ExternalRef == nil || *input.ExternalRef != "" {
			t.Errorf("Update external ref = %v, want a pointer to an empty string", input.ExternalRef)
		}
		return &model.Instance{ID: id, Name: "loja", Status: "disconnected"}, nil
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPatch, "/api/v1/instances/"+id.String(), `{"external_ref":""}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestInstancesUpdateNotFound(t *testing.T) {
	svc := &fakeInstanceService{updateFn: func(context.Context, uuid.UUID, instance.UpdateInput) (*model.Instance, error) {
		return nil, instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodPatch, "/api/v1/instances/"+uuid.NewString(), `{"name":"novo"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesDelete(t *testing.T) {
	id := uuid.New()
	svc := &fakeInstanceService{}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodDelete, "/api/v1/instances/"+id.String(), "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if len(svc.deleteIDs) != 1 || svc.deleteIDs[0] != id {
		t.Errorf("Delete calls = %v, want [%s]", svc.deleteIDs, id)
	}
}

func TestInstancesDeleteNotFound(t *testing.T) {
	svc := &fakeInstanceService{deleteFn: func(context.Context, uuid.UUID) error {
		return instance.ErrNotFound
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodDelete, "/api/v1/instances/"+uuid.NewString(), "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("error code = %q, want %q", code, "not_found")
	}
}

func TestInstancesInternalError(t *testing.T) {
	svc := &fakeInstanceService{getFn: func(context.Context, uuid.UUID) (*model.Instance, error) {
		return nil, context.DeadlineExceeded
	}}

	rec := serveJSON(t, instancesServer(t, svc), http.MethodGet, "/api/v1/instances/"+uuid.NewString(), "")

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
