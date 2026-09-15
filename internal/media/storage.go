// Package media stores message media on the local filesystem with its
// metadata in the database, serving the content until its TTL expires.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/storage"
)

// Errors reported by the storage and mapped to HTTP status codes by the
// handlers.
var (
	// ErrNotFound reports that the requested media does not exist or its
	// content is gone.
	ErrNotFound = errors.New("media not found")
	// ErrExpired reports that the media exists but its retention window ended.
	ErrExpired = errors.New("media expired")
	// ErrEmpty reports that the content to store was empty.
	ErrEmpty = errors.New("media is empty")
	// ErrTooLarge reports that the content exceeds the configured limit.
	ErrTooLarge = errors.New("media exceeds the size limit")
)

// Outbound media kinds, used by the upload handler and the outbound sender.
const (
	KindImage    = "image"
	KindVideo    = "video"
	KindAudio    = "audio"
	KindDocument = "document"
)

// allowedMimes are the media types accepted on upload, mirroring the formats
// supported by WhatsApp for images, video, audio and documents.
var allowedMimes = map[string]struct{}{
	"image/jpeg":                    {},
	"image/png":                     {},
	"image/webp":                    {},
	"video/mp4":                     {},
	"video/3gpp":                    {},
	"audio/aac":                     {},
	"audio/amr":                     {},
	"audio/mpeg":                    {},
	"audio/mp4":                     {},
	"audio/ogg":                     {},
	"application/pdf":               {},
	"text/plain":                    {},
	"application/msword":            {},
	"application/vnd.ms-excel":      {},
	"application/vnd.ms-powerpoint": {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {},
}

// AllowedMime reports whether mimetype is accepted on upload. The comparison
// ignores case and surrounding spaces.
func AllowedMime(mimetype string) bool {
	_, ok := allowedMimes[strings.ToLower(strings.TrimSpace(mimetype))]
	return ok
}

// Kind returns the outbound kind of an allowed mimetype: every image goes as
// an image, every video as a video, every audio as an audio and the remaining
// allowed formats (documents) go as documents. An unaccepted mimetype has no
// kind, matching AllowedMime.
func Kind(mimetype string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(mimetype))
	if _, ok := allowedMimes[normalized]; !ok {
		return "", false
	}
	switch {
	case strings.HasPrefix(normalized, "image/"):
		return KindImage, true
	case strings.HasPrefix(normalized, "video/"):
		return KindVideo, true
	case strings.HasPrefix(normalized, "audio/"):
		return KindAudio, true
	default:
		return KindDocument, true
	}
}

// Storage stores media files under a data dir and records their metadata
// through a repository. A file is written before its row exists and removed
// when the row cannot be created, so a failed save leaves nothing behind.
type Storage struct {
	dir      string
	repo     storage.MediaRepository
	maxBytes int64
	ttl      time.Duration
	now      func() time.Time
}

// NewStorage returns a storage rooted at dir that accepts up to maxBytes per
// media and expires every saved media after ttl.
func NewStorage(dir string, repo storage.MediaRepository, maxBytes int64, ttl time.Duration) *Storage {
	return &Storage{dir: dir, repo: repo, maxBytes: maxBytes, ttl: ttl, now: time.Now}
}

// Save writes data under media/<instance_id>/<media_id>, records its metadata
// and returns the stored row. Empty and over-limit content is rejected before
// anything is written.
func (s *Storage) Save(
	ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte,
) (*model.Media, error) {
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	if int64(len(data)) > s.maxBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(data), s.maxBytes)
	}

	id := uuid.New()
	relPath := filepath.Join("media", instanceID.String(), id.String())
	fullPath := filepath.Join(s.dir, relPath)
	if err := writeMediaFile(fullPath, data); err != nil {
		return nil, fmt.Errorf("save media: %w", err)
	}

	now := s.now()
	sum := sha256.Sum256(data)
	created, err := s.repo.Create(ctx, model.Media{
		ID:          id,
		InstanceID:  instanceID,
		Direction:   direction,
		MessageID:   messageID,
		Mimetype:    mimetype,
		Filename:    sanitizeFilename(filename),
		SizeBytes:   int64(len(data)),
		StoragePath: relPath,
		SHA256:      hex.EncodeToString(sum[:]),
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.ttl),
	})
	if err != nil {
		// The row is the only reference to the file: drop the orphan.
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("save media: %w", mapRepoError(err))
	}
	return created, nil
}

// Open returns the content of the media with the given id along with its
// metadata. An unknown id reports ErrNotFound; a media whose TTL ended reports
// ErrExpired; a row whose file is gone reports ErrNotFound.
func (s *Storage) Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, *model.Media, error) {
	path, record, err := s.Path(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("open media %s: %w", id, ErrNotFound)
		}
		return nil, nil, fmt.Errorf("open media %s: %w", id, err)
	}
	return file, record, nil
}

// Path returns the absolute filesystem path of the media with the given id
// along with its metadata, with the same presence, expiry and path-safety
// checks as Open. It lets a consumer hand the file to something that reads it
// directly instead of streaming it through this storage.
//
// TOCTOU note: the existence Stat below is a best-effort fast-fail only —
// the file can still vanish before the consumer reads it. Callers must
// handle that: Open maps a raced disappearance back to ErrNotFound, and the
// media sender surfaces a session read failure that the outbox retries.
func (s *Storage) Path(ctx context.Context, id uuid.UUID) (string, *model.Media, error) {
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", nil, fmt.Errorf("open media %s: %w", id, mapRepoError(err))
	}
	if !s.now().Before(record.ExpiresAt) {
		return "", nil, fmt.Errorf("open media %s: %w", id, ErrExpired)
	}

	path, err := s.path(record.StoragePath)
	if err != nil {
		return "", nil, fmt.Errorf("open media %s: %w", id, err)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("open media %s: %w", id, ErrNotFound)
		}
		return "", nil, fmt.Errorf("open media %s: %w", id, err)
	}
	return path, record, nil
}

// DeleteByInstance removes every media of an instance, files first: when a
// file cannot be removed its row is kept, so a retry can finish the job
// instead of leaking the content.
func (s *Storage) DeleteByInstance(ctx context.Context, instanceID uuid.UUID) error {
	records, err := s.repo.ListByInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("delete instance media %s: list: %w", instanceID, err)
	}

	var failures []error
	for _, record := range records {
		if err := s.removeFile(record.StoragePath); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("delete instance media %s: remove files: %w", instanceID, err)
	}

	if _, err := s.repo.DeleteByInstance(ctx, instanceID); err != nil {
		return fmt.Errorf("delete instance media %s: delete rows: %w", instanceID, err)
	}

	// Drop the instance directory, including empty parents, temp files left
	// by a crash and any file no row referenced.
	if err := os.RemoveAll(filepath.Join(s.dir, "media", instanceID.String())); err != nil {
		return fmt.Errorf("delete instance media %s: remove directory: %w", instanceID, err)
	}
	return nil
}

// DeleteExpired removes the media whose expiry is due and returns how many
// records were removed. A file that cannot be removed keeps its row, so a
// later pass retries it.
func (s *Storage) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	records, err := s.repo.ListExpired(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired media: list: %w", err)
	}

	removed := 0
	var failures []error
	for _, record := range records {
		if err := s.removeFile(record.StoragePath); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := s.repo.Delete(ctx, record.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
			failures = append(failures, fmt.Errorf("delete media %s: %w", record.ID, err))
			continue
		}
		removed++
	}
	if err := errors.Join(failures...); err != nil {
		return removed, fmt.Errorf("delete expired media: %w", err)
	}
	return removed, nil
}

// path resolves a stored relative path, refusing one that escapes the data
// dir. Paths come from our own rows, so an escaping value means corruption and
// is reported as not found instead of touching the filesystem.
func (s *Storage) path(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrNotFound
	}
	return filepath.Join(s.dir, clean), nil
}

// removeFile drops one stored file. An already missing file is not an error.
func (s *Storage) removeFile(rel string) error {
	path, err := s.path(rel)
	if err != nil {
		return fmt.Errorf("remove media file %q: %w", rel, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove media file %q: %w", rel, err)
	}
	return nil
}

// writeMediaFile writes data at path atomically, so a crash never leaves a
// partial file under the final name.
func writeMediaFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".media-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}

// sanitizeFilename keeps only the base name so a hostile name cannot smuggle
// a path into the metadata; the content path never uses it.
func sanitizeFilename(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		return ""
	}
	return filepath.Base(name)
}

// mapRepoError translates the storage sentinel into the media one, so callers
// do not depend on the repository package.
func mapRepoError(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
