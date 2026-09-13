package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/storage"
)

// fakeMediaRepo is an in-memory storage.MediaRepository for storage tests.
type fakeMediaRepo struct {
	records   map[uuid.UUID]model.Media
	createErr error
	listErr   error
	deleteErr error
}

func newFakeMediaRepo() *fakeMediaRepo {
	return &fakeMediaRepo{records: map[uuid.UUID]model.Media{}}
}

func (f *fakeMediaRepo) Create(_ context.Context, record model.Media) (*model.Media, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.records[record.ID] = record
	copied := record
	return &copied, nil
}

func (f *fakeMediaRepo) Get(_ context.Context, id uuid.UUID) (*model.Media, error) {
	record, ok := f.records[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	copied := record
	return &copied, nil
}

func (f *fakeMediaRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]model.Media, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	records := []model.Media{}
	for _, record := range f.records {
		if record.InstanceID == instanceID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (f *fakeMediaRepo) ListExpired(_ context.Context, now time.Time) ([]model.Media, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	records := []model.Media{}
	for _, record := range f.records {
		if !record.ExpiresAt.After(now) {
			records = append(records, record)
		}
	}
	return records, nil
}

func (f *fakeMediaRepo) Delete(_ context.Context, id uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.records[id]; !ok {
		return storage.ErrNotFound
	}
	delete(f.records, id)
	return nil
}

func (f *fakeMediaRepo) DeleteByInstance(_ context.Context, instanceID uuid.UUID) (int64, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	var removed int64
	for id, record := range f.records {
		if record.InstanceID == instanceID {
			delete(f.records, id)
			removed++
		}
	}
	return removed, nil
}

// seedFile writes data at dir/rel, creating the parent directories.
func seedFile(t *testing.T, dir, rel string, data []byte) {
	t.Helper()

	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create media dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
}

// countFiles returns how many regular files live under dir.
func countFiles(t *testing.T, dir string) int {
	t.Helper()

	count := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return count
}

func TestStorageSaveAndOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1<<20, time.Hour)
	instanceID := uuid.New()
	messageID := uuid.NewString()
	payload := []byte("conteúdo da mídia")

	saved, err := store.Save(ctx, instanceID, "inbound", messageID, "image/jpeg", "foto.jpg", payload)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if saved.ID == uuid.Nil {
		t.Error("Save: ID is nil")
	}
	if saved.ID.Version() != 4 {
		t.Errorf("Save: ID %s is not a random UUIDv4", saved.ID)
	}
	if saved.InstanceID != instanceID {
		t.Errorf("Save: InstanceID = %s, want %s", saved.InstanceID, instanceID)
	}
	if saved.Direction != "inbound" {
		t.Errorf("Save: Direction = %q, want inbound", saved.Direction)
	}
	if saved.MessageID != messageID {
		t.Errorf("Save: MessageID = %q, want %q", saved.MessageID, messageID)
	}
	if saved.Mimetype != "image/jpeg" {
		t.Errorf("Save: Mimetype = %q, want image/jpeg", saved.Mimetype)
	}
	if saved.Filename != "foto.jpg" {
		t.Errorf("Save: Filename = %q, want foto.jpg", saved.Filename)
	}
	if saved.SizeBytes != int64(len(payload)) {
		t.Errorf("Save: SizeBytes = %d, want %d", saved.SizeBytes, len(payload))
	}
	wantHash := sha256.Sum256(payload)
	if saved.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Errorf("Save: SHA256 = %q, want %q", saved.SHA256, hex.EncodeToString(wantHash[:]))
	}
	if saved.CreatedAt.IsZero() {
		t.Error("Save: CreatedAt is zero")
	}
	if want := saved.CreatedAt.Add(time.Hour); !saved.ExpiresAt.Equal(want) {
		t.Errorf("Save: ExpiresAt = %v, want %v", saved.ExpiresAt, want)
	}

	wantPath := filepath.Join("media", instanceID.String(), saved.ID.String())
	if saved.StoragePath != wantPath {
		t.Errorf("Save: StoragePath = %q, want %q", saved.StoragePath, wantPath)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, wantPath))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Errorf("stored bytes = %q, want %q", onDisk, payload)
	}

	body, opened, err := store.Open(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = body.Close() }()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read opened media: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Open bytes = %q, want %q", got, payload)
	}
	if opened.ID != saved.ID || opened.Mimetype != saved.Mimetype || opened.SizeBytes != saved.SizeBytes {
		t.Errorf("Open metadata = %+v, want id %s, mimetype %q, size %d",
			opened, saved.ID, saved.Mimetype, saved.SizeBytes)
	}
}

func TestStorageSaveOpaqueIDs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStorage(dir, newFakeMediaRepo(), 1<<20, time.Hour)
	instanceID := uuid.New()

	first, err := store.Save(ctx, instanceID, "outbound", "", "image/png", "a.png", []byte("a"))
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := store.Save(ctx, instanceID, "outbound", "", "image/png", "b.png", []byte("b"))
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}

	if first.ID == second.ID {
		t.Error("two saved media share the same ID")
	}
	if first.ID.Version() != 4 || second.ID.Version() != 4 {
		t.Errorf("IDs %s/%s are not random UUIDv4", first.ID, second.ID)
	}
}

func TestStorageSaveRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1024, time.Hour)

	for name, data := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Save(context.Background(), uuid.New(), "outbound", "", "image/png", "x.png", data)
			if !errors.Is(err, ErrEmpty) {
				t.Fatalf("Save error = %v, want ErrEmpty", err)
			}
		})
	}

	if files := countFiles(t, dir); files != 0 {
		t.Errorf("rejected Save left %d file(s) behind", files)
	}
	if len(repo.records) != 0 {
		t.Errorf("rejected Save left %d record(s) behind", len(repo.records))
	}
}

func TestStorageSaveEnforcesMaxBytes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStorage(dir, newFakeMediaRepo(), 8, time.Hour)

	if _, err := store.Save(ctx, uuid.New(), "outbound", "", "image/png", "big.png",
		bytes.Repeat([]byte("a"), 9)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over-limit Save error = %v, want ErrTooLarge", err)
	}
	if _, err := store.Save(ctx, uuid.New(), "outbound", "", "image/png", "exact.png",
		bytes.Repeat([]byte("a"), 8)); err != nil {
		t.Fatalf("Save at the limit: %v", err)
	}
}

func TestStorageSaveRemovesFileWhenRepositoryFails(t *testing.T) {
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	repo.createErr = errors.New("insert failed")
	store := NewStorage(dir, repo, 1024, time.Hour)

	if _, err := store.Save(context.Background(), uuid.New(), "inbound", "", "image/png", "x.png", []byte("data")); err == nil {
		t.Fatal("Save succeeded with a failing repository")
	}
	if files := countFiles(t, dir); files != 0 {
		t.Errorf("failed Save left %d file(s) behind", files)
	}
}

func TestStorageSaveSanitizesFilename(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStorage(dir, newFakeMediaRepo(), 1024, time.Hour)
	instanceID := uuid.New()

	saved, err := store.Save(ctx, instanceID, "outbound", "", "application/pdf", "../../etc/passwd", []byte("data"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Filename != "passwd" {
		t.Errorf("Filename = %q, want the base name passwd", saved.Filename)
	}

	unnamed, err := store.Save(ctx, instanceID, "outbound", "", "application/pdf", "  ", []byte("data"))
	if err != nil {
		t.Fatalf("Save without filename: %v", err)
	}
	if unnamed.Filename != "" {
		t.Errorf("Filename = %q, want empty", unnamed.Filename)
	}
}

func TestStorageOpenErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1024, time.Hour)
	instanceID := uuid.New()

	saved, err := store.Save(ctx, instanceID, "inbound", "", "image/jpeg", "foto.jpg", []byte("conteúdo"))
	if err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	t.Run("unknown id", func(t *testing.T) {
		if _, _, err := store.Open(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open error = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		expired := model.Media{
			ID:          uuid.New(),
			InstanceID:  instanceID,
			ExpiresAt:   time.Now().Add(-time.Minute),
			StoragePath: filepath.Join("media", instanceID.String(), "expired"),
		}
		seedFile(t, dir, expired.StoragePath, []byte("old"))
		if _, err := repo.Create(ctx, expired); err != nil {
			t.Fatalf("seed expired record: %v", err)
		}
		if _, _, err := store.Open(ctx, expired.ID); !errors.Is(err, ErrExpired) {
			t.Errorf("Open error = %v, want ErrExpired", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, saved.StoragePath)); err != nil {
			t.Fatalf("remove stored file: %v", err)
		}
		if _, _, err := store.Open(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open error = %v, want ErrNotFound", err)
		}
	})

	t.Run("path escapes the data dir", func(t *testing.T) {
		escape := model.Media{
			ID:          uuid.New(),
			InstanceID:  instanceID,
			ExpiresAt:   time.Now().Add(time.Hour),
			StoragePath: filepath.Join("..", "etc", "passwd"),
		}
		if _, err := repo.Create(ctx, escape); err != nil {
			t.Fatalf("seed escaping record: %v", err)
		}
		if _, _, err := store.Open(ctx, escape.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open error = %v, want ErrNotFound", err)
		}
	})
}

func TestStorageDeleteExpiredRemovesFileAndRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1024, time.Hour)
	instanceID := uuid.New()
	now := time.Now()

	expired := model.Media{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		Direction:   "inbound",
		Mimetype:    "image/png",
		SizeBytes:   3,
		StoragePath: filepath.Join("media", instanceID.String(), "expired"),
		ExpiresAt:   now.Add(-time.Minute),
	}
	fresh := model.Media{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		Direction:   "inbound",
		Mimetype:    "image/png",
		SizeBytes:   3,
		StoragePath: filepath.Join("media", instanceID.String(), "fresh"),
		ExpiresAt:   now.Add(time.Hour),
	}
	seedFile(t, dir, expired.StoragePath, []byte("old"))
	seedFile(t, dir, fresh.StoragePath, []byte("new"))
	for _, record := range []model.Media{expired, fresh} {
		if _, err := repo.Create(ctx, record); err != nil {
			t.Fatalf("seed record: %v", err)
		}
	}

	removed, err := store.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeleteExpired removed = %d, want 1", removed)
	}

	if _, err := os.Stat(filepath.Join(dir, expired.StoragePath)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expired file still present: %v", err)
	}
	if _, ok := repo.records[expired.ID]; ok {
		t.Error("expired record still stored")
	}
	if _, err := os.Stat(filepath.Join(dir, fresh.StoragePath)); err != nil {
		t.Errorf("fresh file missing: %v", err)
	}
	if _, ok := repo.records[fresh.ID]; !ok {
		t.Error("fresh record was removed")
	}
}

func TestStorageDeleteExpiredKeepsRowWhenFileRemovalFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1024, time.Hour)
	now := time.Now()

	escaping := model.Media{
		ID:          uuid.New(),
		InstanceID:  uuid.New(),
		StoragePath: filepath.Join("..", "outside"),
		ExpiresAt:   now.Add(-time.Minute),
	}
	if _, err := repo.Create(ctx, escaping); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	removed, err := store.DeleteExpired(ctx, now)
	if err == nil {
		t.Error("DeleteExpired succeeded with an unremovable path")
	}
	if removed != 0 {
		t.Errorf("DeleteExpired removed = %d, want 0", removed)
	}
	if _, ok := repo.records[escaping.ID]; !ok {
		t.Error("row was deleted even though its file could not be removed")
	}
}

func TestStorageDeleteByInstanceRemovesFilesAndRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1<<20, time.Hour)
	instanceA, instanceB := uuid.New(), uuid.New()

	first, err := store.Save(ctx, instanceA, "inbound", "", "image/jpeg", "a.jpg", []byte("a"))
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := store.Save(ctx, instanceA, "outbound", "", "image/jpeg", "b.jpg", []byte("b"))
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	kept, err := store.Save(ctx, instanceB, "inbound", "", "image/jpeg", "c.jpg", []byte("c"))
	if err != nil {
		t.Fatalf("third Save: %v", err)
	}

	if err := store.DeleteByInstance(ctx, instanceA); err != nil {
		t.Fatalf("DeleteByInstance: %v", err)
	}

	for _, record := range []*model.Media{first, second} {
		if _, err := os.Stat(filepath.Join(dir, record.StoragePath)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("file %s still present: %v", record.StoragePath, err)
		}
		if _, ok := repo.records[record.ID]; ok {
			t.Errorf("record %s still stored", record.ID)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, kept.StoragePath)); err != nil {
		t.Errorf("other instance file missing: %v", err)
	}
	if _, ok := repo.records[kept.ID]; !ok {
		t.Error("other instance record was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "media", instanceA.String())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("instance media dir still present: %v", err)
	}
}

func TestStoragePathReturnsStoredFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStorage(dir, newFakeMediaRepo(), 1<<20, time.Hour)
	instanceID := uuid.New()
	payload := []byte("conteúdo da mídia")

	saved, err := store.Save(ctx, instanceID, "outbound", "", "image/jpeg", "foto.jpg", payload)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, record, err := store.Path(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(dir, saved.StoragePath); path != want {
		t.Errorf("Path = %q, want %q", path, want)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("Path = %q, want an absolute path", path)
	}
	if record.ID != saved.ID || record.Mimetype != saved.Mimetype || record.SizeBytes != saved.SizeBytes {
		t.Errorf("Path metadata = %+v, want the saved record %+v", record, saved)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read path: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Errorf("file bytes = %q, want %q", onDisk, payload)
	}
}

func TestStoragePathErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := newFakeMediaRepo()
	store := NewStorage(dir, repo, 1024, time.Hour)
	instanceID := uuid.New()

	saved, err := store.Save(ctx, instanceID, "outbound", "", "image/jpeg", "foto.jpg", []byte("conteúdo"))
	if err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	t.Run("unknown id", func(t *testing.T) {
		if _, _, err := store.Path(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Errorf("Path error = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		expired := model.Media{
			ID:          uuid.New(),
			InstanceID:  instanceID,
			ExpiresAt:   time.Now().Add(-time.Minute),
			StoragePath: filepath.Join("media", instanceID.String(), "expired"),
		}
		seedFile(t, dir, expired.StoragePath, []byte("old"))
		if _, err := repo.Create(ctx, expired); err != nil {
			t.Fatalf("seed expired record: %v", err)
		}
		if _, _, err := store.Path(ctx, expired.ID); !errors.Is(err, ErrExpired) {
			t.Errorf("Path error = %v, want ErrExpired", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, saved.StoragePath)); err != nil {
			t.Fatalf("remove stored file: %v", err)
		}
		if _, _, err := store.Path(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Path error = %v, want ErrNotFound", err)
		}
	})
}

func TestKind(t *testing.T) {
	tests := []struct {
		mimetype string
		want     string
		ok       bool
	}{
		{mimetype: "image/jpeg", want: KindImage, ok: true},
		{mimetype: "IMAGE/PNG", want: KindImage, ok: true},
		{mimetype: "video/mp4", want: KindVideo, ok: true},
		{mimetype: "video/3gpp", want: KindVideo, ok: true},
		{mimetype: "audio/ogg", want: KindAudio, ok: true},
		{mimetype: "audio/mpeg", want: KindAudio, ok: true},
		{mimetype: "application/pdf", want: KindDocument, ok: true},
		{mimetype: "text/plain", want: KindDocument, ok: true},
		{mimetype: "application/octet-stream", ok: false},
		{mimetype: "image/svg+xml", ok: false},
		{mimetype: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.mimetype, func(t *testing.T) {
			got, ok := Kind(tt.mimetype)
			if ok != tt.ok || got != tt.want {
				t.Errorf("Kind(%q) = (%q, %v), want (%q, %v)", tt.mimetype, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAllowedMime(t *testing.T) {
	allowed := []string{"image/jpeg", "image/png", "image/webp", "video/mp4", "audio/ogg", "application/pdf", " IMAGE/JPEG "}
	for _, mimetype := range allowed {
		if !AllowedMime(mimetype) {
			t.Errorf("AllowedMime(%q) = false, want true", mimetype)
		}
	}

	rejected := []string{"", "text/html", "application/x-executable", "image/svg+xml", "application/octet-stream"}
	for _, mimetype := range rejected {
		if AllowedMime(mimetype) {
			t.Errorf("AllowedMime(%q) = true, want false", mimetype)
		}
	}
}
