package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// usersCRUDServer wires the users endpoints behind Authenticate with the
// given stores and creation-time default quota.
func usersCRUDServer(t *testing.T, users storage.UserRepository, keys storage.APIKeyRepository, defaultQuota int) *http.Server {
	t.Helper()
	return New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, DefaultUserQuota: defaultQuota},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Users:        users,
			Keys:         keys,
			JWTSecret:    testJWTSecret,
		},
	)
}

// assertNoSecrets fails when the raw JSON response leaks password material.
func assertNoSecrets(t *testing.T, body []byte) {
	t.Helper()
	lowered := strings.ToLower(string(body))
	for _, leak := range []string{"password", "hash"} {
		if strings.Contains(lowered, leak) {
			t.Errorf("response leaks %q: %q", leak, body)
		}
	}
}

func TestUsersCreateHappy(t *testing.T) {
	t.Run("explicit quota", func(t *testing.T) {
		users := newFakeUserRepository()
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users.byID[admin.ID] = admin
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 4)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"cliente@example.com","password":"s3cret-password","role":"user","instance_quota":7}`,
			cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		assertNoSecrets(t, rec.Body.Bytes())
		data := decodeUserData(t, rec.Body.Bytes())
		if got := userDataString(t, data, "email"); got != "cliente@example.com" {
			t.Errorf("data.email = %q, want %q", got, "cliente@example.com")
		}
		if got := userDataString(t, data, "role"); got != "user" {
			t.Errorf("data.role = %q, want %q", got, "user")
		}
		if got := userDataInt(t, data, "instance_quota"); got != 7 {
			t.Errorf("data.instance_quota = %d, want 7", got)
		}
		idRaw, ok := data["id"]
		if !ok {
			t.Fatal("data is missing id")
		}
		id, err := uuid.Parse(strings.Trim(string(idRaw), `"`))
		if err != nil {
			t.Fatalf("data.id is not a uuid: %q", idRaw)
		}

		stored, err := users.GetByEmail(context.Background(), "cliente@example.com")
		if err != nil {
			t.Fatalf("GetByEmail: %v", err)
		}
		if stored.ID != id {
			t.Errorf("stored id = %s, want %s", stored.ID, id)
		}
		if err := auth.CheckPassword(stored.PasswordHash, "s3cret-password"); err != nil {
			t.Errorf("stored hash does not verify the password: %v", err)
		}
	})

	t.Run("absent quota applies the creation-time default", func(t *testing.T) {
		users := newFakeUserRepository()
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users.byID[admin.ID] = admin
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 4)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"semcota@example.com","password":"s3cret-password","role":"user"}`,
			cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		data := decodeUserData(t, rec.Body.Bytes())
		if got := userDataInt(t, data, "instance_quota"); got != 4 {
			t.Errorf("data.instance_quota = %d, want the default 4", got)
		}
	})

	t.Run("explicit zero stays unlimited despite the default", func(t *testing.T) {
		users := newFakeUserRepository()
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users.byID[admin.ID] = admin
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 4)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"zero@example.com","password":"s3cret-password","role":"user","instance_quota":0}`,
			cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		data := decodeUserData(t, rec.Body.Bytes())
		if got := userDataInt(t, data, "instance_quota"); got != 0 {
			t.Errorf("data.instance_quota = %d, want the explicit 0", got)
		}
	})

	t.Run("global key may create", func(t *testing.T) {
		users := newFakeUserRepository()
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)

		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"global@example.com","password":"s3cret-password","role":"admin"}`,
			nil, testToken, nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		assertNoSecrets(t, rec.Body.Bytes())
	})
}

func TestUsersCreateValidation(t *testing.T) {
	cases := map[string]struct {
		body       string
		wantStatus int
		wantCode   string
	}{
		"missing email":    {`{"password":"s3cret-password","role":"user"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"empty email":      {`{"email":"","password":"s3cret-password","role":"user"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"missing password": {`{"email":"a@example.com","role":"user"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"empty password":   {`{"email":"a@example.com","password":"","role":"user"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"missing role":     {`{"email":"a@example.com","password":"s3cret-password"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"invalid role":     {`{"email":"a@example.com","password":"s3cret-password","role":"owner"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"uppercase role":   {`{"email":"a@example.com","password":"s3cret-password","role":"Admin"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"negative quota":   {`{"email":"a@example.com","password":"s3cret-password","role":"user","instance_quota":-1}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"string quota":     {`{"email":"a@example.com","password":"s3cret-password","role":"user","instance_quota":"many"}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"float quota":      {`{"email":"a@example.com","password":"s3cret-password","role":"user","instance_quota":1.5}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"bool quota":       {`{"email":"a@example.com","password":"s3cret-password","role":"user","instance_quota":true}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"null quota":       {`{"email":"a@example.com","password":"s3cret-password","role":"user","instance_quota":null}`, http.StatusUnprocessableEntity, "unprocessable_entity"},
		"malformed body":   {`{"email":`, http.StatusBadRequest, "invalid_request"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			users := newFakeUserRepository()
			admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
			users.byID[admin.ID] = admin
			srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
			cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

			rec := serveRBAC(t, srv, http.MethodPost, "/users", tc.body, cookie, "", nil)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != tc.wantCode {
				t.Errorf("error code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestUsersCreateDuplicate(t *testing.T) {
	seed := func(t *testing.T) (*fakeUserRepository, *http.Server, *http.Cookie) {
		t.Helper()
		hash, err := auth.HashPassword("s3cret-password")
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		users := newFakeUserRepository(&model.User{
			ID: uuid.New(), Email: "Cliente@example.com", PasswordHash: hash, Role: "user",
		})
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users.byID[admin.ID] = admin
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
		return users, srv, rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))
	}

	t.Run("exact duplicate", func(t *testing.T) {
		users, srv, cookie := seed(t)
		before := len(users.byID)
		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"Cliente@example.com","password":"other-password","role":"user"}`,
			cookie, "", nil)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
			t.Errorf("error code = %q, want %q", code, "conflict")
		}
		if len(users.byID) != before {
			t.Errorf("users = %d, want %d (duplicate must not insert)", len(users.byID), before)
		}
	})

	t.Run("case variant", func(t *testing.T) {
		_, srv, cookie := seed(t)
		rec := serveRBAC(t, srv, http.MethodPost, "/users",
			`{"email":"cliente@EXAMPLE.com","password":"other-password","role":"user"}`,
			cookie, "", nil)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
			t.Errorf("error code = %q, want %q", code, "conflict")
		}
	})
}

func TestUsersListGet(t *testing.T) {
	older := &model.User{ID: uuid.New(), Email: "older@example.com", Role: "user", InstanceQuota: 1,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	newer := &model.User{ID: uuid.New(), Email: "newer@example.com", Role: "admin", InstanceQuota: 2,
		CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin",
		CreatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}

	newServer := func(t *testing.T) (*http.Server, *http.Cookie) {
		t.Helper()
		users := newFakeUserRepository(older, newer)
		users.byID[admin.ID] = admin
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
		return srv, rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))
	}

	t.Run("list returns all in created_at order without secrets", func(t *testing.T) {
		srv, cookie := newServer(t)
		rec := serveRBAC(t, srv, http.MethodGet, "/users", "", cookie, "", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		assertNoSecrets(t, rec.Body.Bytes())
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		decodeJSON(t, rec.Body.Bytes(), &payload)
		if len(payload.Data) != 3 {
			t.Fatalf("items = %d, want 3 (body %q)", len(payload.Data), rec.Body.String())
		}
		if payload.Data[0]["email"] != "older@example.com" || payload.Data[1]["email"] != "newer@example.com" ||
			payload.Data[2]["email"] != "admin@example.com" {
			t.Errorf("order = %v %v %v, want older then newer then admin",
				payload.Data[0]["email"], payload.Data[1]["email"], payload.Data[2]["email"])
		}
		for i, item := range payload.Data {
			for _, field := range []string{"id", "email", "role", "instance_quota"} {
				if _, ok := item[field]; !ok {
					t.Errorf("items[%d] is missing %q", i, field)
				}
			}
		}
	})

	t.Run("get one", func(t *testing.T) {
		srv, cookie := newServer(t)
		rec := serveRBAC(t, srv, http.MethodGet, "/users/"+older.ID.String(), "", cookie, "", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
		}
		assertNoSecrets(t, rec.Body.Bytes())
		data := decodeUserData(t, rec.Body.Bytes())
		if got := userDataString(t, data, "email"); got != "older@example.com" {
			t.Errorf("data.email = %q, want %q", got, "older@example.com")
		}
	})
}

func TestUsersUnknownIDsAdmin(t *testing.T) {
	admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
	users := newFakeUserRepository()
	users.byID[admin.ID] = admin
	srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
	cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))
	unknown := uuid.NewString()

	for name, tc := range map[string]struct {
		method string
		path   string
		body   string
	}{
		"get unknown":      {http.MethodGet, "/users/" + unknown, ""},
		"get malformed":    {http.MethodGet, "/users/not-a-uuid", ""},
		"patch unknown":    {http.MethodPatch, "/users/" + unknown, `{"instance_quota":1}`},
		"delete unknown":   {http.MethodDelete, "/users/" + unknown, ""},
		"delete malformed": {http.MethodDelete, "/users/not-a-uuid", ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveRBAC(t, srv, tc.method, tc.path, tc.body, cookie, "", nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
				t.Errorf("error code = %q, want %q", code, "not_found")
			}
		})
	}
}

// fkDeleteUsers wraps the fake user store with a Delete that surfaces a
// Postgres foreign-key violation, covering the check-then-delete race where
// CountByOwner saw zero rows but the delete still hits a new instance row.
type fkDeleteUsers struct {
	*fakeUserRepository
}

func (f *fkDeleteUsers) Delete(_ context.Context, id uuid.UUID) error {
	return &pgconn.PgError{Code: "23503", Message: `violates foreign key "instances_owner_user_id_fkey"`}
}

var _ storage.UserRepository = (*fkDeleteUsers)(nil)

func TestUsersDelete(t *testing.T) {
	t.Run("plain user is removed and cannot log in afterwards", func(t *testing.T) {
		hash, err := auth.HashPassword("s3cret-password")
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		target := &model.User{ID: uuid.New(), Email: "saindo@example.com", PasswordHash: hash, Role: "user"}
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users := newFakeUserRepository(target, admin)
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodDelete, "/users/"+target.ID.String(), "", cookie, "", nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("body = %q, want empty on 204", rec.Body.String())
		}

		if _, err := users.GetByID(context.Background(), target.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetByID after delete = %v, want %v", err, storage.ErrNotFound)
		}

		login := serveAuthForUsers(t, srv, `{"email":"saindo@example.com","password":"s3cret-password"}`)
		if login.Code != http.StatusUnauthorized {
			t.Fatalf("login after delete status = %d, want %d (body %q)", login.Code, http.StatusUnauthorized, login.Body.String())
		}
	})

	t.Run("owner with instances is kept", func(t *testing.T) {
		owner := &model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user", InstanceQuota: 5}
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users := newFakeUserRepository(owner, admin)
		keys := &countingKeys{instances: []model.Instance{ownedInstance(owner.ID, "disconnected")}}
		srv := usersCRUDServer(t, users, keys, 0)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodDelete, "/users/"+owner.ID.String(), "", cookie, "", nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
			t.Errorf("error code = %q, want %q", code, "conflict")
		}

		if _, err := users.GetByID(context.Background(), owner.ID); err != nil {
			t.Errorf("owner is gone after the denied delete: %v", err)
		}
	})

	t.Run("foreign-key race still conflicts", func(t *testing.T) {
		owner := &model.User{ID: uuid.New(), Email: "corrida@example.com", Role: "user"}
		admin := &model.User{ID: uuid.New(), Email: "admin@example.com", Role: "admin"}
		users := &fkDeleteUsers{newFakeUserRepository(owner, admin)}
		srv := usersCRUDServer(t, users, &fakeAPIKeyRepository{}, 0)
		cookie := rbacSessionCookie(mustSessionToken(t, admin.ID, "admin"))

		rec := serveRBAC(t, srv, http.MethodDelete, "/users/"+owner.ID.String(), "", cookie, "", nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "conflict" {
			t.Errorf("error code = %q, want %q", code, "conflict")
		}
	})
}

// serveAuthForUsers posts a login body against srv without credentials.
func serveAuthForUsers(t *testing.T, srv *http.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestUsersDeniedMatrix(t *testing.T) {
	adminID := uuid.New()
	userID := uuid.New()
	targetID := uuid.New()
	instID := uuid.New()
	const liveKey = "live-instance-key-for-users-crud"

	newServer := func(t *testing.T) *http.Server {
		t.Helper()
		users := newFakeUserRepository(
			&model.User{ID: adminID, Email: "admin@example.com", Role: "admin"},
			&model.User{ID: userID, Email: "cliente@example.com", Role: "user", InstanceQuota: 5},
			&model.User{ID: targetID, Email: "alvo@example.com", Role: "user", InstanceQuota: 5},
		)
		keys := &fakeAPIKeyRepository{byHash: map[string]uuid.UUID{apikeyHashOf(liveKey): instID}}
		return usersCRUDServer(t, users, keys, 0)
	}

	endpoints := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/users", `{"email":"novo@example.com","password":"s3cret-password","role":"user"}`},
		{"list", http.MethodGet, "/users", ""},
		{"get one", http.MethodGet, "/users/" + targetID.String(), ""},
		{"patch quota", http.MethodPatch, "/users/" + targetID.String(), `{"instance_quota":1}`},
		{"delete", http.MethodDelete, "/users/" + targetID.String(), ""},
	}

	userCookie := func(t *testing.T) *http.Cookie {
		t.Helper()
		return rbacSessionCookie(mustSessionToken(t, userID, "user"))
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Run("user session is forbidden", func(t *testing.T) {
				srv := newServer(t)
				rec := serveRBAC(t, srv, ep.method, ep.path, ep.body, userCookie(t), "", nil)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
					t.Errorf("error code = %q, want %q", code, "forbidden")
				}
			})

			t.Run("instance key is forbidden", func(t *testing.T) {
				srv := newServer(t)
				rec := serveRBAC(t, srv, ep.method, ep.path, ep.body, nil, liveKey, nil)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
					t.Errorf("error code = %q, want %q", code, "forbidden")
				}
			})

			t.Run("anonymous is unauthorized", func(t *testing.T) {
				srv := newServer(t)
				req := httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body))
				rec := httptest.NewRecorder()
				srv.Handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
				}
				if code := errorCode(t, rec.Body.Bytes()); code != "unauthorized" {
					t.Errorf("error code = %q, want %q", code, "unauthorized")
				}
			})
		})
	}

	t.Run("non-privileged scope cannot probe unknown ids", func(t *testing.T) {
		srv := newServer(t)
		unknown := "/users/" + uuid.NewString()
		for name, tc := range map[string]struct {
			method string
			body   string
		}{
			"get":    {http.MethodGet, ""},
			"patch":  {http.MethodPatch, `{"instance_quota":1}`},
			"delete": {http.MethodDelete, ""},
		} {
			t.Run(name+" as user", func(t *testing.T) {
				rec := serveRBAC(t, srv, tc.method, unknown, tc.body, userCookie(t), "", nil)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
				}
			})
			t.Run(name+" as instance key", func(t *testing.T) {
				rec := serveRBAC(t, srv, tc.method, unknown, tc.body, nil, liveKey, nil)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
				}
			})
		}
	})
}
