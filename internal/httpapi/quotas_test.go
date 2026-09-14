package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// countingKeys is an in-memory storage.APIKeyRepository whose counts derive
// from an explicit instance list with no status filter, so disconnected rows
// count toward quotas exactly like the Postgres counts of task 1.2.
type countingKeys struct {
	byHash    map[string]uuid.UUID
	instances []model.Instance
	countErr  error
}

func (f *countingKeys) SetHash(_ context.Context, instanceID uuid.UUID, hash string) error {
	if f.byHash == nil {
		f.byHash = map[string]uuid.UUID{}
	}
	f.byHash[hash] = instanceID
	return nil
}

func (f *countingKeys) InstanceByHash(_ context.Context, hash string) (uuid.UUID, error) {
	if id, ok := f.byHash[hash]; ok {
		return id, nil
	}
	return uuid.Nil, storage.ErrNotFound
}

func (f *countingKeys) ClearHash(_ context.Context, instanceID uuid.UUID) error {
	for hash, id := range f.byHash {
		if id == instanceID {
			delete(f.byHash, hash)
		}
	}
	return nil
}

// CountByOwner counts every owned row, any status.
func (f *countingKeys) CountByOwner(_ context.Context, owner uuid.UUID) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	var count int
	for _, inst := range f.instances {
		if inst.OwnerUserID != nil && *inst.OwnerUserID == owner {
			count++
		}
	}
	return count, nil
}

// CountAll counts every row, any status.
func (f *countingKeys) CountAll(_ context.Context) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.instances), nil
}

// quotaTestServer wires svc with quota-aware deps behind Authenticate.
func quotaTestServer(t *testing.T, maxInstances int, users storage.UserRepository, keys storage.APIKeyRepository) (*http.Server, *fakeInstanceService) {
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

func quotaUser(id uuid.UUID, email, role string, quota int) *model.User {
	return &model.User{ID: id, Email: email, Role: role, InstanceQuota: quota}
}

func ownedInstance(owner uuid.UUID, status string) model.Instance {
	return model.Instance{ID: uuid.New(), Name: "loja", Status: status, OwnerUserID: &owner}
}

func TestQuotaGlobalMaxBlocksUserButNotAdminOrGlobal(t *testing.T) {
	userID := uuid.New()
	adminID := uuid.New()
	users := newFakeUserRepository(
		quotaUser(userID, "cliente@example.com", "user", 10),
		quotaUser(adminID, "admin@example.com", "admin", 10),
	)
	other := uuid.New()
	// The global ceiling is reached (2 >= 2) while the user still has quota
	// headroom (0 owned of 10).
	keys := &countingKeys{instances: []model.Instance{
		ownedInstance(other, "connected"),
		ownedInstance(other, "disconnected"),
	}}
	srv, svc := quotaTestServer(t, 2, users, keys)

	t.Run("quota-rich user is denied", func(t *testing.T) {
		cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "quota_exceeded" {
			t.Errorf("error code = %q, want %q", code, "quota_exceeded")
		}
		if len(svc.createInputs) != 0 {
			t.Errorf("Create calls = %d, want none when the global quota denies", len(svc.createInputs))
		}
	})

	t.Run("admin session bypasses", func(t *testing.T) {
		cookie := rbacSessionCookie(mustSessionToken(t, adminID, "admin"))
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})

	t.Run("global scope bypasses", func(t *testing.T) {
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, nil, testToken, nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})
}

func TestQuotaUserQuotaExceededBlocksIncludingDisconnected(t *testing.T) {
	userID := uuid.New()
	users := newFakeUserRepository(quotaUser(userID, "cliente@example.com", "user", 1))
	// The single owned row is disconnected: every existing instance counts,
	// any state, so the user at 1 of 1 is denied.
	keys := &countingKeys{instances: []model.Instance{ownedInstance(userID, "disconnected")}}
	srv, svc := quotaTestServer(t, 0, users, keys)

	cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "quota_exceeded" {
		t.Errorf("error code = %q, want %q", code, "quota_exceeded")
	}
	if len(svc.createInputs) != 0 {
		t.Errorf("Create calls = %d, want none when the user quota denies", len(svc.createInputs))
	}
}

func TestQuotaZeroMeansUnlimited(t *testing.T) {
	userID := uuid.New()

	t.Run("user quota zero skips the per-user check", func(t *testing.T) {
		users := newFakeUserRepository(quotaUser(userID, "cliente@example.com", "user", 0))
		keys := &countingKeys{instances: []model.Instance{
			ownedInstance(userID, "disconnected"),
			ownedInstance(userID, "disconnected"),
			ownedInstance(userID, "connected"),
		}}
		srv, _ := quotaTestServer(t, 10, users, keys)

		cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})

	t.Run("global zero skips the global check", func(t *testing.T) {
		users := newFakeUserRepository(quotaUser(userID, "cliente@example.com", "user", 100))
		keys := &countingKeys{instances: []model.Instance{
			ownedInstance(uuid.New(), "disconnected"),
			ownedInstance(uuid.New(), "disconnected"),
		}}
		srv, _ := quotaTestServer(t, 0, users, keys)

		cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
		rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
		}
	})
}

func TestQuotaWithinLimitsCreates(t *testing.T) {
	userID := uuid.New()
	users := newFakeUserRepository(quotaUser(userID, "cliente@example.com", "user", 2))
	keys := &countingKeys{instances: []model.Instance{ownedInstance(userID, "disconnected")}}
	srv, svc := quotaTestServer(t, 5, users, keys)

	cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(svc.createInputs) != 1 {
		t.Errorf("Create calls = %d, want 1 when quotas allow", len(svc.createInputs))
	}
}

// TestQuotaIgnoresDefaultQuotaAtCheckTime guards the binding that
// WZAP_DEFAULT_USER_INSTANCE_QUOTA is creation-time only: a stored quota of 0
// stays unlimited at check time even with instances present.
func TestQuotaIgnoresDefaultQuotaAtCheckTime(t *testing.T) {
	userID := uuid.New()
	users := newFakeUserRepository(quotaUser(userID, "cliente@example.com", "user", 0))
	keys := &countingKeys{instances: []model.Instance{ownedInstance(userID, "disconnected")}}
	// No DefaultUserQuota reaches the handler: only MaxInstances is wired, so
	// the stored 0 must stay unlimited.
	srv, _ := quotaTestServer(t, 0, users, keys)

	cookie := rbacSessionCookie(mustSessionToken(t, userID, "user"))
	rec := serveRBAC(t, srv, http.MethodPost, "/instances", `{"name":"loja"}`, cookie, "", nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

var _ storage.APIKeyRepository = (*countingKeys)(nil)
