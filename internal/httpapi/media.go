package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/media"
	"wzap/internal/model"
)

// MediaStore is the media storage contract consumed by the handlers: Open
// serves a download and Save stores an upload. *media.Storage implements it.
type MediaStore interface {
	Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, *model.Media, error)
	Save(
		ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte,
	) (*model.Media, error)
}

// handleGetMedia streams one media content. The success body is the raw
// content, not the JSON envelope used by the other endpoints. The media loads
// first (missing or expired → 404) and the owning instance authorizes next
// (403) under the instance-scoped rule.
func handleGetMedia(instances InstanceService, store MediaStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			Error(w, r, http.StatusNotFound, "not_found", "media not found")
			return
		}

		body, record, err := store.Open(r.Context(), id)
		if err != nil {
			writeMediaError(w, r, err)
			return
		}
		defer func() { _ = body.Close() }()

		owner, err := instances.Get(r.Context(), record.InstanceID)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, owner); err != nil {
			writeForbidden(w, r)
			return
		}

		w.Header().Set("Content-Type", record.Mimetype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mediaContentDisposition(record))
		w.Header().Set("Content-Length", strconv.FormatInt(record.SizeBytes, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, body)
	}
}

// mediaContentDisposition builds an attachment disposition with a sanitized
// filename: the name is sender controlled, so it must never be trusted as a
// path or as a header value. An empty name falls back to the media id.
func mediaContentDisposition(record *model.Media) string {
	name := sanitizeMediaFilename(record.Filename)
	if name == "" {
		name = record.ID.String()
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		return "attachment"
	}
	return disposition
}

// sanitizeMediaFilename reduces a sender supplied filename to its base name
// without control characters, so it cannot traverse paths or inject headers.
func sanitizeMediaFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	switch name {
	case "", ".", "..", "/":
		return ""
	}
	return name
}

// writeMediaError maps a media storage error to its HTTP status and error
// envelope. Unknown and expired media are indistinguishable to the client.
// Scope denials answer 403.
func writeMediaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		Error(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, media.ErrNotFound), errors.Is(err, media.ErrExpired):
		Error(w, r, http.StatusNotFound, "not_found", "media not found")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
