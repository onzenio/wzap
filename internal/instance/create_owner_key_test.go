package instance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session/sessiontest"
	"wzap/internal/storage"
)

// fakeUserRepo is an in-memory storage.UserRepository with List order fully
// controlled by the test (the order given is the created_at order).
type fakeUserRepo struct {
	users   []model.User
	byID    map[uuid.UUID]*model.User
	listErr error
	getErr  error
}

func newFakeUserRepo(users ...model.User) *fakeUserRepo {
	f := &fakeUserRepo{byID: map[uuid.UUID]*model.User{}}
	for i := range users {
		f.users = append(f.users, users[i])
		user := f.users[len(f.users)-1]
		f.byID[user.ID] = &user
	}
	return f
}

func (f *fakeUserRepo) Create(_ context.Context, user model.User) (*model.User, error) {
	f.users = append(f.users, user)
	stored := f.users[len(f.users)-1]
	f.byID[user.ID] = &stored
	return &stored, nil
}

func (f *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if user, ok := f.byID[id]; ok {
		return user, nil
	}
	return nil, storage.ErrNotFound
}

func (f *fakeUserRepo) GetByEmail(_ context.Context, _ string) (*model.User, error) {
	return nil, storage.ErrNotFound
}

func (f *fakeUserRepo) List(_ context.Context) ([]model.User, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]model.User(nil), f.users...), nil
}

func (f *fakeUserRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(f.byID, id)
	return nil
}

func (f *fakeUserRepo) Count(_ context.Context) (int, error) { return len(f.users), nil }

// fakeKeyRepo is an in-memory storage.APIKeyRepository recording stored hashes.
type fakeKeyRepo struct {
	hashes map[uuid.UUID]string
	setErr error
}

func newFakeKeyRepo() *fakeKeyRepo { return &fakeKeyRepo{hashes: map[uuid.UUID]string{}} }

func (f *fakeKeyRepo) SetHash(_ context.Context, instanceID uuid.UUID, hash string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.hashes[instanceID] = hash
	return nil
}

func (f *fakeKeyRepo) InstanceByHash(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, storage.ErrNotFound
}

func (f *fakeKeyRepo) ClearHash(_ context.Context, instanceID uuid.UUID) error {
	delete(f.hashes, instanceID)
	return nil
}

func (f *fakeKeyRepo) CountByOwner(_ context.Context, _ uuid.UUID) (int, error) { return 0, nil }

func (f *fakeKeyRepo) CountAll(_ context.Context) (int, error) { return 0, nil }

func ownerPtr(id uuid.UUID) *uuid.UUID { return &id }

func TestServiceCreateEmitsKeyAndPersistsOwnerAndHash(t *testing.T) {
	repo := newFakeRepo()
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
	users := newFakeUserRepo(owner)
	keys := newFakeKeyRepo()
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, users, keys)

	created, key, err := svc.Create(context.Background(), CreateInput{
		Name: "loja", ExternalRef: "crm-1", OwnerUserID: ownerPtr(owner.ID),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if key == "" {
		t.Fatal("plaintext key is empty, want the one-time instance key")
	}
	if created.OwnerUserID == nil || *created.OwnerUserID != owner.ID {
		t.Errorf("OwnerUserID = %v, want %s", created.OwnerUserID, owner.ID)
	}
	stored := repo.instances[created.ID]
	if stored.OwnerUserID == nil || *stored.OwnerUserID != owner.ID {
		t.Errorf("stored OwnerUserID = %v, want %s", stored.OwnerUserID, owner.ID)
	}

	hash, ok := keys.hashes[created.ID]
	if !ok {
		t.Fatal("keys repo holds no hash for the created instance")
	}
	if hash == key {
		t.Error("keys repo holds the plaintext key, want only its hash")
	}
	sum := sha256.Sum256([]byte(key))
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash = %q, want the sha256 of the returned key", hash)
	}
	if created.ID == uuid.Nil {
		t.Error("ID = nil, want a generated UUID")
	}
}

func TestServiceCreateRequiresOwner(t *testing.T) {
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{}, newFakeUserRepo(), newFakeKeyRepo())

	_, _, err := svc.Create(context.Background(), CreateInput{Name: "loja"})
	if !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Create error = %v, want ErrOwnerRequired", err)
	}
}

func TestServiceCreateUnknownOwner(t *testing.T) {
	users := newFakeUserRepo(model.User{ID: uuid.New(), Email: "outro@example.com", Role: "user"})
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{}, users, newFakeKeyRepo())

	unknown := uuid.New()
	_, _, err := svc.Create(context.Background(), CreateInput{Name: "loja", OwnerUserID: ownerPtr(unknown)})
	if !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("Create error = %v, want ErrOwnerNotFound", err)
	}
}

func TestServiceCreateStoresHashOnly(t *testing.T) {
	repo := newFakeRepo()
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "admin"}
	users := newFakeUserRepo(owner)
	keys := newFakeKeyRepo()
	svc := NewService(repo, sessiontest.New(nil), &fakeMedia{}, users, keys)

	created, key, err := svc.Create(context.Background(), CreateInput{Name: "loja", OwnerUserID: ownerPtr(owner.ID)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, hash := range keys.hashes {
		if hash == key {
			t.Error("keys repo holds the plaintext key, want only its hash")
		}
	}
	stored := repo.instances[created.ID]
	_ = stored
}

func TestServiceCreateHashFailure(t *testing.T) {
	owner := model.User{ID: uuid.New(), Email: "dono@example.com", Role: "user"}
	users := newFakeUserRepo(owner)
	keys := newFakeKeyRepo()
	keys.setErr = errors.New("database down")
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{}, users, keys)

	_, key, err := svc.Create(context.Background(), CreateInput{Name: "loja", OwnerUserID: ownerPtr(owner.ID)})
	if err == nil {
		t.Fatal("Create error = nil, want the hash storage failure")
	}
	if key != "" {
		t.Errorf("plaintext key = %q, want it withheld when the hash was not stored", key)
	}
}

func TestServiceOldestAdmin(t *testing.T) {
	adminFirst := model.User{ID: uuid.New(), Email: "primeiro@example.com", Role: "admin"}
	adminSecond := model.User{ID: uuid.New(), Email: "segundo@example.com", Role: "admin"}
	plain := model.User{ID: uuid.New(), Email: "cliente@example.com", Role: "user"}
	// List order is created_at order: the plain user comes first, so the
	// lookup must skip it and return the first admin in list order.
	users := newFakeUserRepo(plain, adminFirst, adminSecond)
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{}, users, newFakeKeyRepo())

	got, err := svc.OldestAdmin(context.Background())
	if err != nil {
		t.Fatalf("OldestAdmin: %v", err)
	}
	if got != adminFirst.ID {
		t.Errorf("OldestAdmin = %s, want the first admin in list order %s", got, adminFirst.ID)
	}
}

func TestServiceOldestAdminWithoutAdmin(t *testing.T) {
	users := newFakeUserRepo(model.User{ID: uuid.New(), Email: "cliente@example.com", Role: "user"})
	svc := NewService(newFakeRepo(), sessiontest.New(nil), &fakeMedia{}, users, newFakeKeyRepo())

	_, err := svc.OldestAdmin(context.Background())
	if !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("OldestAdmin error = %v, want ErrNoAdmin", err)
	}
}
