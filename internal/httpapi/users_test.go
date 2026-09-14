package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// quotaUserStore is an in-memory storage.UserRepository with a working
// UpdateQuota, so the PATCH tests exercise the quota edit against the same
// rows the create handler reads.
type quotaUserStore struct {
	byID    map[uuid.UUID]*model.User
	byEmail map[string]*model.User
}

func newQuotaUserStore(users ...*model.User) *quotaUserStore {
	f := &quotaUserStore{byID: map[uuid.UUID]*model.User{}, byEmail: map[string]*model.User{}}
	for _, u := range users {
		f.byID[u.ID] = u
		f.byEmail[strings.ToLower(u.Email)] = u
	}
	return f
}

func (f *quotaUserStore) Create(_ context.Context, user model.User) (*model.User, error) {
	f.byID[user.ID] = &user
	f.byEmail[strings.ToLower(user.Email)] = &user
	return &user, nil
}

func (f *quotaUserStore) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, storage.ErrNotFound
}

func (f *quotaUserStore) GetByEmail(_ context.Context, email string) (*model.User, error) {
	if u, ok := f.byEmail[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, storage.ErrNotFound
}

func (f *quotaUserStore) List(context.Context) ([]model.User, error) { return nil, nil }

func (f *quotaUserStore) Delete(_ context.Context, id uuid.UUID) error {
	delete(f.byID, id)
	return nil
}

func (f *quotaUserStore) Count(context.Context) (int, error) { return len(f.byID), nil }

// UpdateQuota stores the new per-user quota, reporting storage.ErrNotFound
// for an unknown user like the Postgres implementation.
func (f *quotaUserStore) UpdateQuota(_ context.Context, id uuid.UUID, quota int) error {
	u, ok := f.byID[id]
	if !ok {
		return fmt.Errorf("update user quota: %w", storage.ErrNotFound)
	}
	u.InstanceQuota = quota
	return nil
}

var _ storage.UserRepository = (*quotaUserStore)(nil)

// patchQuotaServer wires the PATCH quota endpoint together with the
// quota-gated create handler, sharing one user store so a PATCH takes effect
// on the next create.
func patchQuotaServer(t *testing.T, maxInstances int, users storage.UserRepository, keys storage.APIKeyRepository) (*http.Server, *fakeInstanceService) {
	t.Helper()
	svc := &fakeInstanceService{createFn: echoCreateFn("quota-key-1")}
	srv := New(
		config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken, JWTSecret: testJWTSecret, MaxInstances: maxInstances},
		discardLogger(),
		Deps{
			ReadyChecker: checkFunc(func(context.Context) error { return nil }),
			Instances:    svc,
			Users:        users,
			Keys:         keys,
			JWTSecret:    testJWTSecret,
		},
	)
	return srv, svc
}

func decodeUserData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	decodeJSON(t, body, &payload)
	return payload.Data
}

func userDataString(t *testing.T, data map[string]json.RawMessage, field string) string {
	t.Helper()
	raw, ok := data[field]
	if !ok {
		t.Fatalf("data is missing %q", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode data.%s of %q: %v", field, raw, err)
	}
	return value
}

func userDataInt(t *testing.T, data map[string]json.RawMessage, field string) int {
	t.Helper()
	raw, ok := data[field]
	if !ok {
		t.Fatalf("data is missing %q", field)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode data.%s of %q: %v", field, raw, err)
	}
	return value
}

func TestUsersPatchQuotaHappy(t *testing.T) {
	userID := uuid.New()
	adminID := uuid.New()
	patchers := map[string]func(t *testing.T) (*http.Cookie, string){
		"admin session": func(t *testing.T) (*http.Cookie, string) {
			t.Helper()
			return rbacSessionCookie(mustSessionToken(t, adminID, "admin")), ""
		},
		"global key": func(t *testing.T) (*http.Cookie, string) {
			t.Helper()
			return nil, testToken
		},
	}

	for name, patcher := range patchers {
		t.Run(name, func(t *testing.T) {
			users := newQuotaUserStore(
				&model.User{ID: userID, Email: "cliente@example.com", Role: "user", InstanceQuota: 5},
				&model.User{ID: adminID, Email: "admin@example.com", Role: "admin", InstanceQuota: 5},
			)
			keys := &countingKeys{instances: []model.Instance{ownedInstance(userID, "disconnected")}}
			srv, svc := patchQuotaServer(t, 0, users, keys)

			cookie, apiKey := patcher(t)
			rec := serveRBAC(t, srv, http.MethodPatch, "/users/"+userID.String(), `{"instance_quota":1}`, cookie, apiKey, nil)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
			}
			data := decodeUserData(t, rec.Body.Bytes())
			if got := userDataString(t, data, "id"); got != userID.String() {
				t.Errorf("data.id = %q, want %s", got, userID)
			}
			if got := userDataString(t, data, "email"); got != "cliente@example.com" {
				t.Errorf("data.email = %q, want %q", got, "cliente@example.com")
			}
			if got := userDataString(t, data, "role"); got != "user" {
				t.Errorf("data.role = %q, want %q", got, "user")
			}
			if got := userDataInt(t, data, "instance_quota"); got != 1 {
				t.Errorf("data.instance_quota = %d, want 1", got)
			}

			// The edited quota takes effect on the next create: the user owns
			// 1 disconnected instance against the new quota of 1.
			userCookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
			denied := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, userCookie, "", nil)
			if denied.Code != http.StatusForbidden {
				t.Fatalf("create after PATCH status = %d, want %d (body %q)", denied.Code, http.StatusForbidden, denied.Body.String())
			}
			if code := errorCode(t, denied.Body.Bytes()); code != "quota_exceeded" {
				t.Errorf("create after PATCH error code = %q, want %q", code, "quota_exceeded")
			}
			if len(svc.createInputs) != 0 {
				t.Errorf("Create calls = %d, want none once the patched quota denies", len(svc.createInputs))
			}
		})
	}
}

func TestUsersPatchQuotaForbidden(t *testing.T) {
	userID := uuid.New()
	targetID := uuid.New()
	instID := uuid.New()
	const liveKey = "live-instance-key-for-patch"

	users := newQuotaUserStore(
		&model.User{ID: userID, Email: "cliente@example.com", Role: "user", InstanceQuota: 5},
		&model.User{ID: targetID, Email: "alvo@example.com", Role: "user", InstanceQuota: 5},
	)
	keys := &countingKeys{byHash: map[string]uuid.UUID{apikeyHashOf(liveKey): instID}}
	srv := func() *http.Server {
		s, _ := patchQuotaServer(t, 0, users, keys)
		return s
	}()
	path := "/users/" + targetID.String()

	t.Run("user session is forbidden", func(t *testing.T) {
		cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
		rec := serveRBAC(t, srv, http.MethodPatch, path, `{"instance_quota":1}`, cookie, "", nil)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("instance key is forbidden", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPatch, path, `{"instance_quota":1}`, nil, liveKey, nil)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "forbidden" {
			t.Errorf("error code = %q, want %q", code, "forbidden")
		}
	})

	t.Run("quota is unchanged after denials", func(t *testing.T) {
		got, err := users.GetByID(context.Background(), targetID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.InstanceQuota != 5 {
			t.Errorf("InstanceQuota = %d, want 5 (denied PATCH must not write)", got.InstanceQuota)
		}
	})
}

func TestUsersPatchQuotaNotFound(t *testing.T) {
	adminID := uuid.New()
	users := newQuotaUserStore(
		&model.User{ID: adminID, Email: "admin@example.com", Role: "admin", InstanceQuota: 5},
	)
	srv, _ := patchQuotaServer(t, 0, users, &countingKeys{})

	t.Run("unknown user", func(t *testing.T) {
		cookie := rbacSessionCookie(mustSessionToken(t, adminID, "admin"))
		rec := serveRBAC(t, srv, http.MethodPatch, "/users/"+uuid.NewString(), `{"instance_quota":1}`, cookie, "", nil)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
			t.Errorf("error code = %q, want %q", code, "not_found")
		}
	})

	t.Run("malformed id", func(t *testing.T) {
		cookie := rbacSessionCookie(mustSessionToken(t, adminID, "admin"))
		rec := serveRBAC(t, srv, http.MethodPatch, "/users/not-a-uuid", `{"instance_quota":1}`, cookie, "", nil)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
			t.Errorf("error code = %q, want %q", code, "not_found")
		}
	})
}

func TestUsersPatchQuotaInvalid(t *testing.T) {
	adminID := uuid.New()
	userID := uuid.New()

	cases := map[string]string{
		"negative":     `{"instance_quota":-1}`,
		"string":       `{"instance_quota":"many"}`,
		"float":        `{"instance_quota":1.5}`,
		"bool":         `{"instance_quota":true}`,
		"null":         `{"instance_quota":null}`,
		"missing":      `{}`,
		"empty object": `{"other":1}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			users := newQuotaUserStore(
				&model.User{ID: userID, Email: "cliente@example.com", Role: "user", InstanceQuota: 5},
				&model.User{ID: adminID, Email: "admin@example.com", Role: "admin", InstanceQuota: 5},
			)
			srv, _ := patchQuotaServer(t, 0, users, &countingKeys{})

			cookie := rbacSessionCookie(mustSessionToken(t, adminID, "admin"))
			rec := serveRBAC(t, srv, http.MethodPatch, "/users/"+userID.String(), body, cookie, "", nil)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
			}
			if code := errorCode(t, rec.Body.Bytes()); code != "unprocessable_entity" {
				t.Errorf("error code = %q, want %q", code, "unprocessable_entity")
			}

			got, err := users.GetByID(context.Background(), userID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if got.InstanceQuota != 5 {
				t.Errorf("InstanceQuota = %d, want 5 (rejected PATCH must not write)", got.InstanceQuota)
			}
		})
	}
}

func TestUsersPatchQuotaRequiresAuth(t *testing.T) {
	users := newQuotaUserStore()
	srv, _ := patchQuotaServer(t, 0, users, &countingKeys{})

	req := httptest.NewRequest(http.MethodPatch, "/users/"+uuid.NewString(), strings.NewReader(`{"instance_quota":1}`))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
