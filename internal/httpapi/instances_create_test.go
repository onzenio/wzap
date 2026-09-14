package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/instance"
	"wzap/internal/model"
)

// createOwnerTestServer wires svc behind Authenticate with both the global key
// and session cookies accepted, so the create owner/key tests can act as each
// scope.
func createOwnerTestServer(t *testing.T, svc InstanceService) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    svc,
			Users:        newFakeUserRepository(),
			Keys:         &fakeAPIKeyRepository{},
			JWTSecret:    testJWTSecret,
		},
	)
}

func mustSessionToken(t *testing.T, userID uuid.UUID, role string) string {
	t.Helper()
	token, err := auth.MintToken(userID, role, testJWTSecret)
	if err != nil {
		t.Fatalf("MintToken(%s): %v", role, err)
	}
	return token
}

// createData decodes the data envelope of rec into a raw field map.
func createData(t *testing.T, recBody []byte) map[string]json.RawMessage {
	t.Helper()
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	decodeJSON(t, recBody, &payload)
	return payload.Data
}

func rawString(t *testing.T, field json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(field, &value); err != nil {
		t.Fatalf("decode string field %q: %v", field, err)
	}
	return value
}

// echoCreateFn returns a createFn echoing the resolved owner with key.
func echoCreateFn(key string) func(context.Context, instance.CreateInput) (*model.Instance, string, error) {
	return func(_ context.Context, input instance.CreateInput) (*model.Instance, string, error) {
		return &model.Instance{
			ID: uuid.New(), Name: input.Name, ExternalRef: input.ExternalRef,
			Status: "disconnected", OwnerUserID: input.OwnerUserID,
		}, key, nil
	}
}

func TestInstancesCreateByUserSessionEmitsOwnerAndKey(t *testing.T) {
	svc := &fakeInstanceService{createFn: echoCreateFn("user-key-1")}
	srv := createOwnerTestServer(t, svc)
	userID := uuid.New()
	cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))

	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	data := createData(t, rec.Body.Bytes())
	ownerRaw, ok := data["owner_user_id"]
	if !ok {
		t.Fatal("data is missing owner_user_id")
	}
	if got := rawString(t, ownerRaw); got != userID.String() {
		t.Errorf("data.owner_user_id = %q, want the session user %s", got, userID)
	}
	keyRaw, ok := data["instance_api_key"]
	if !ok {
		t.Fatal("data is missing instance_api_key")
	}
	if got := rawString(t, keyRaw); got != "user-key-1" {
		t.Errorf("data.instance_api_key = %q, want the one-time plaintext key", got)
	}
	if len(svc.createInputs) != 1 || svc.createInputs[0].OwnerUserID == nil ||
		*svc.createInputs[0].OwnerUserID != userID {
		t.Errorf("Create owner = %+v, want the session user %s", svc.createInputs, userID)
	}
}

func TestInstancesCreateByGlobalDefaultsToOldestAdmin(t *testing.T) {
	oldest := uuid.New()
	svc := &fakeInstanceService{
		oldestAdminFn: func(context.Context) (uuid.UUID, error) { return oldest, nil },
		createFn:      echoCreateFn("global-key-1"),
	}
	srv := createOwnerTestServer(t, svc)

	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, testToken, nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	data := createData(t, rec.Body.Bytes())
	ownerRaw, ok := data["owner_user_id"]
	if !ok {
		t.Fatal("data is missing owner_user_id")
	}
	if got := rawString(t, ownerRaw); got != oldest.String() {
		t.Errorf("data.owner_user_id = %q, want the oldest admin %s", got, oldest)
	}
	if keyRaw, ok := data["instance_api_key"]; !ok || string(keyRaw) == `""` {
		t.Error("data.instance_api_key is missing or empty, want the one-time plaintext key")
	}
}

func TestInstancesCreateWithOwnerOverride(t *testing.T) {
	override := uuid.New()
	adminID := uuid.New()
	newSvc := func() *fakeInstanceService {
		return &fakeInstanceService{createFn: echoCreateFn("override-key-1")}
	}
	adminCookie := rbacSessionCookie(mustSessionToken(t, adminID, "admin"))

	t.Run("global with override", func(t *testing.T) {
		svc := newSvc()
		srv := createOwnerTestServer(t, svc)

		rec := serveRBAC(t, srv, http.MethodPost, "/instances",
			`{"name":"loja","owner_user_id":"`+override.String()+`"}`, nil, testToken, nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		data := createData(t, rec.Body.Bytes())
		ownerRaw, ok := data["owner_user_id"]
		if !ok {
			t.Fatal("data is missing owner_user_id")
		}
		if got := rawString(t, ownerRaw); got != override.String() {
			t.Errorf("data.owner_user_id = %q, want the override %s", got, override)
		}
		if keyRaw, ok := data["instance_api_key"]; !ok || string(keyRaw) == `""` {
			t.Error("data.instance_api_key is missing or empty, want the one-time plaintext key")
		}
		if len(svc.createInputs) != 1 || svc.createInputs[0].OwnerUserID == nil ||
			*svc.createInputs[0].OwnerUserID != override {
			t.Errorf("Create owner = %+v, want the override %s", svc.createInputs, override)
		}
	})

	t.Run("admin session with override", func(t *testing.T) {
		svc := newSvc()
		srv := createOwnerTestServer(t, svc)

		rec := serveRBAC(t, srv, http.MethodPost, "/instances",
			`{"name":"loja","owner_user_id":"`+override.String()+`"}`, adminCookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		data := createData(t, rec.Body.Bytes())
		ownerRaw, ok := data["owner_user_id"]
		if !ok {
			t.Fatal("data is missing owner_user_id")
		}
		if got := rawString(t, ownerRaw); got != override.String() {
			t.Errorf("data.owner_user_id = %q, want the override %s", got, override)
		}
		if keyRaw, ok := data["instance_api_key"]; !ok || string(keyRaw) == `""` {
			t.Error("data.instance_api_key is missing or empty, want the one-time plaintext key")
		}
	})
}

func TestInstancesCreateUserOverrideForbidden(t *testing.T) {
	svc := &fakeInstanceService{createFn: echoCreateFn("must-not-issue")}
	srv := createOwnerTestServer(t, svc)
	cookie := rbacSessionCookie(mustSessionToken(t, uuid.New(), "user"))

	rec := serveRBAC(t, srv, http.MethodPost, "/instances",
		`{"name":"loja","owner_user_id":"`+uuid.NewString()+`"}`, cookie, "", nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
		t.Errorf("error code = %q, want %q", code, "forbidden")
	}
	if len(svc.createInputs) != 0 {
		t.Errorf("Create calls = %v, want none for a user override", svc.createInputs)
	}
}

func TestInstancesCreateUnknownOverrideUnprocessable(t *testing.T) {
	svc := &fakeInstanceService{
		createFn: func(_ context.Context, _ instance.CreateInput) (*model.Instance, string, error) {
			return nil, "", instance.ErrOwnerNotFound
		},
	}
	srv := createOwnerTestServer(t, svc)

	rec := serveRBAC(t, srv, http.MethodPost, "/instances",
		`{"name":"loja","owner_user_id":"`+uuid.NewString()+`"}`, nil, testToken, nil)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unprocessable_entity" {
		t.Errorf("error code = %q, want %q", code, "unprocessable_entity")
	}
}

func TestInstancesCreateNoAdminInternalError(t *testing.T) {
	svc := &fakeInstanceService{
		oldestAdminFn: func(context.Context) (uuid.UUID, error) {
			return uuid.Nil, instance.ErrNoAdmin
		},
	}
	srv := createOwnerTestServer(t, svc)

	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, testToken, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "internal_error" {
		t.Errorf("error code = %q, want %q", code, "internal_error")
	}
	if len(svc.createInputs) != 0 {
		t.Errorf("Create calls = %v, want none when no admin exists", svc.createInputs)
	}
}

func TestInstancesCreateKeyShownOnce(t *testing.T) {
	userID := uuid.New()
	owned := func(id uuid.UUID) *model.Instance {
		return &model.Instance{ID: id, Name: "loja", Status: "disconnected", OwnerUserID: &userID}
	}
	svc := &fakeInstanceService{
		createFn: echoCreateFn("shown-once-key"),
		getFn: func(_ context.Context, id uuid.UUID) (*model.Instance, error) {
			return owned(id), nil
		},
		listFn: func(context.Context, int, string) ([]model.Instance, string, error) {
			return []model.Instance{*owned(uuid.New())}, "", nil
		},
		updateFn: func(_ context.Context, id uuid.UUID, _ instance.UpdateInput) (*model.Instance, error) {
			return owned(id), nil
		},
	}
	srv := createOwnerTestServer(t, svc)
	cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))

	created := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body %q)", created.Code, http.StatusCreated, created.Body.String())
	}
	data := createData(t, created.Body.Bytes())
	keyRaw, ok := data["instance_api_key"]
	if !ok || string(keyRaw) == `""` {
		t.Fatalf("create response is missing the one-time instance_api_key (body %q)", created.Body.String())
	}
	idRaw, ok := data["id"]
	if !ok {
		t.Fatalf("create response is missing id (body %q)", created.Body.String())
	}
	id := rawString(t, idRaw)

	assertNoKey := func(name string, body []byte) {
		t.Helper()
		if name == "list" {
			var list struct {
				Data struct {
					Items []map[string]json.RawMessage `json:"items"`
				} `json:"data"`
			}
			decodeJSON(t, body, &list)
			if len(list.Data.Items) == 0 {
				t.Fatalf("%s has no items, want the owned instance listed", name)
			}
			for i, item := range list.Data.Items {
				if _, found := item["instance_api_key"]; found {
					t.Errorf("%s items[%d] exposes instance_api_key, want it exactly once at create", name, i)
				}
			}
			return
		}
		var payload struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		decodeJSON(t, body, &payload)
		if _, found := payload.Data["instance_api_key"]; found {
			t.Errorf("%s exposes instance_api_key, want it exactly once at create", name)
		}
	}

	get := serveRBAC(t, srv, http.MethodGet, "/instances/"+id, "", cookie, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d (body %q)", get.Code, http.StatusOK, get.Body.String())
	}
	assertNoKey("get", get.Body.Bytes())

	list := serveRBAC(t, srv, http.MethodGet, "/instances", "", cookie, "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d (body %q)", list.Code, http.StatusOK, list.Body.String())
	}
	assertNoKey("list", list.Body.Bytes())

	updated := serveRBAC(t, srv, http.MethodPatch, "/instances/"+id, `{"name":"novo"}`, cookie, "", nil)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body %q)", updated.Code, http.StatusOK, updated.Body.String())
	}
	assertNoKey("update", updated.Body.Bytes())
}
