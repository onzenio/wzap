package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/storage"
)

const (
	// idempotencyKeyHeader carries the caller supplied key. It is optional:
	// without it a send behaves exactly as before.
	idempotencyKeyHeader = "Idempotency-Key"
	// idempotentReplayHeader marks a response replayed from the idempotency
	// store instead of freshly produced.
	idempotentReplayHeader = "X-Idempotent-Replay"
	// idempotencyTTL is how long a stored response stays replayable.
	idempotencyTTL = 24 * time.Hour
	// idempotencyKeyMaxLen caps the key size accepted from clients.
	idempotencyKeyMaxLen = 255
	// fingerprintBodyLimit bounds the JSON body the middleware buffers to
	// compute the request fingerprint. A bigger body falls back to a
	// method+route fingerprint.
	fingerprintBodyLimit = 1 << 20
	// fingerprintMultipartOverhead is the slack above the configured upload
	// limit that still gets an exact multipart fingerprint: the multipart
	// boundaries, part headers and text fields around the file content.
	fingerprintMultipartOverhead = 1 << 20
	// fingerprintMultipartFallback bounds the multipart body the middleware
	// spools when no upload limit was configured. A bigger body falls back to
	// a method+route fingerprint.
	fingerprintMultipartFallback = 64 << 20
)

// Idempotency makes a retried send safe: the first request stores its response
// under the caller's key and a repeat replays it instead of enqueueing the same
// message twice. It scopes every key to the instance of the route and is meant
// to wrap only the send POSTs.
//
// Without the header the request goes straight through. While the original
// request runs the key is in flight and a repeat gets 409; a key reused with
// different content gets 422. A 4xx releases the key so the caller can fix the
// payload and retry; a 5xx is stored and replayed because the send may have
// reached WhatsApp before failing, so re-running it could duplicate the
// message. A 503 is the exception: the send path answers it before reaching
// WhatsApp, so the key is released and the retry the body asks for is possible.
// A panic or a handler that writes nothing also releases it.
//
// maxUploadBytes is the largest multipart upload accepted by the routes the
// middleware wraps; it lets a media upload be fingerprinted exactly instead of
// falling back to the route. A non-positive value uses a generous fallback.
func Idempotency(
	repo storage.IdempotencyRepository, log *slog.Logger, maxUploadBytes int64,
) func(http.Handler) http.Handler {
	multipartLimit := maxUploadBytes + fingerprintMultipartOverhead
	if maxUploadBytes <= 0 {
		multipartLimit = fingerprintMultipartFallback
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if repo == nil || r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			key := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > idempotencyKeyMaxLen {
				Error(w, r, http.StatusBadRequest, "invalid_request", "Idempotency-Key exceeds 255 characters")
				return
			}

			// A route without a valid instance cannot be scoped, so the handler
			// answers as usual (typically 404) without touching the key.
			targetID, err := uuid.Parse(r.PathValue("id"))
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			fingerprint, cleanup, err := fingerprintRequest(r, multipartLimit)
			if cleanup != nil {
				// The spooled upload is only needed while the handler runs.
				defer cleanup()
			}
			if err != nil {
				Error(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
				return
			}

			record, acquired, err := repo.Acquire(r.Context(), targetID, key, fingerprint, time.Now().Add(idempotencyTTL))
			switch {
			case errors.Is(err, storage.ErrInProgress):
				Error(w, r, http.StatusConflict, "conflict", "request with this Idempotency-Key is already in progress")
				return
			case errors.Is(err, storage.ErrFingerprintMismatch):
				Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "Idempotency-Key was already used with different content")
				return
			case err != nil:
				// The store must not be the reason a send is dropped: log and
				// let the request through unprotected.
				log.WarnContext(r.Context(), "idempotency store unavailable, proceeding without it",
					"request_id", RequestIDFromContext(r.Context()),
					"error", err,
				)
				next.ServeHTTP(w, r)
				return
			}

			if !acquired {
				replay(w, record)
				return
			}

			capture := &responseCapture{ResponseWriter: w}
			defer func() {
				// The client may have disconnected by now; the outcome still has
				// to be recorded, so the cleanup ignores the request cancellation.
				ctx := context.WithoutCancel(r.Context())
				if !capture.wrote {
					releaseKey(ctx, repo, log, targetID, key)
					return
				}

				// A 4xx means the message never left, so the key is freed and
				// the caller can fix the payload and retry. A 503 comes from
				// the not-ready path before WhatsApp, so it is freed too: the
				// body tells the caller to retry, and replaying the 503 until
				// the key expired would make that instruction impossible.
				status := capture.status
				if (status >= http.StatusBadRequest && status < http.StatusInternalServerError) ||
					status == http.StatusServiceUnavailable {
					releaseKey(ctx, repo, log, targetID, key)
					return
				}
				// Any other result, including a 5xx, is stored and replayed: the
				// send may have reached WhatsApp before failing, so re-running
				// it could duplicate the message.
				if err := repo.Complete(ctx, targetID, key, status, capture.body.Bytes()); err != nil {
					log.WarnContext(ctx, "store idempotent response",
						"request_id", RequestIDFromContext(ctx),
						"error", err,
					)
				}
			}()

			next.ServeHTTP(capture, r)
		})
	}
}

// replay answers with a response stored under an idempotency key.
func replay(w http.ResponseWriter, record *model.IdempotencyRecord) {
	status := record.ResponseStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(idempotentReplayHeader, "true")
	w.WriteHeader(status)
	_, _ = w.Write(record.ResponseBody)
}

// releaseKey frees an idempotency key, logging a failure to free it.
func releaseKey(ctx context.Context, repo storage.IdempotencyRepository, log *slog.Logger, instanceID uuid.UUID, key string) {
	if err := repo.Release(ctx, instanceID, key); err != nil {
		log.WarnContext(ctx, "release idempotency key",
			"request_id", RequestIDFromContext(ctx),
			"error", err,
		)
	}
}

// responseCapture records the status and body written by the wrapped handler so
// the middleware can store them under the idempotency key.
type responseCapture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	wrote  bool
}

// WriteHeader records the first status written and forwards it.
func (c *responseCapture) WriteHeader(status int) {
	if c.wrote {
		return
	}
	c.wrote = true
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

// Write records the body bytes and forwards them, defaulting to 200.
func (c *responseCapture) Write(b []byte) (int, error) {
	if !c.wrote {
		c.wrote = true
		c.status = http.StatusOK
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (c *responseCapture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// fingerprintRequest returns a stable hash of a send: method, route and body,
// plus a cleanup function the caller must run once the handler is done with the
// request body (nil when nothing has to be released).
//
// A multipart body is parsed as it streams: its text fields and each file's
// name, content type and content hash take part in the fingerprint, so a
// same-key retry is only a replay when the upload is really the same. The body
// is spooled to a temporary file while it is parsed and handed back to the
// handler, so file content is never held in memory. A body above the
// configured upload limit falls back to a method+route fingerprint, which is
// harmless because the handler rejects it before anything is enqueued.
//
// A non-multipart body larger than fingerprintBodyLimit is also hashed by
// method and route only, and is left intact for the handler.
func fingerprintRequest(r *http.Request, multipartLimit int64) (string, func(), error) {
	contentType := r.Header.Get("Content-Type")
	if mediaType, params, err := mime.ParseMediaType(contentType); err == nil && strings.HasPrefix(mediaType, "multipart/") {
		return fingerprintMultipart(r, params["boundary"], multipartLimit)
	}

	body, complete, err := readBodyPrefix(r)
	if err != nil {
		return "", nil, err
	}
	if !complete {
		return routeFingerprint(r), nil, nil
	}

	sum := sha256.New()
	writeRoute(sum, r)
	sum.Write(body)
	return hex.EncodeToString(sum.Sum(nil)), nil, nil
}

// fingerprintMultipart spools the multipart body of r while hashing its parts,
// then restores it for the handler. The cleanup closes and removes the spool.
func fingerprintMultipart(r *http.Request, boundary string, limit int64) (string, func(), error) {
	if boundary == "" {
		return "", nil, errors.New("multipart body without a boundary")
	}

	spool, err := os.CreateTemp("", ".wzap-fingerprint-*")
	if err != nil {
		// The body cannot be parsed and restored without a spool: degrade to
		// the route fingerprint and leave it for the handler.
		return routeFingerprint(r), nil, nil
	}
	cleanup := func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}

	written, err := io.Copy(spool, io.LimitReader(r.Body, limit+1))
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if written > limit {
		// Too large to fingerprint exactly: the handler will reject it, so
		// restore the body and fall back to the route.
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return "", nil, err
		}
		r.Body = io.NopCloser(io.MultiReader(spool, r.Body))
		return routeFingerprint(r), cleanup, nil
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return "", nil, err
	}
	parts, err := multipartParts(boundary, spool)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return "", nil, err
	}
	r.Body = spool
	return partsFingerprint(r, parts), cleanup, nil
}

// routeFingerprint hashes the method and route of r.
func routeFingerprint(r *http.Request) string {
	sum := sha256.New()
	writeRoute(sum, r)
	return hex.EncodeToString(sum.Sum(nil))
}

// partsFingerprint hashes the method, route and sorted multipart parts. Each
// part is length-prefixed, so a value containing the part separator cannot be
// re-segmented into a different set of parts.
func partsFingerprint(r *http.Request, parts []string) string {
	sum := sha256.New()
	writeRoute(sum, r)
	for _, part := range parts {
		_, _ = fmt.Fprintf(sum, "%d:", len(part))
		sum.Write([]byte(part))
		sum.Write([]byte{'\n'})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// writeRoute writes the method and route pattern of r into h. The matched
// pattern is preferred over the raw path so the instance id does not take part
// in the fingerprint.
func writeRoute(h io.Writer, r *http.Request) {
	route := r.Pattern
	if route == "" {
		route = r.URL.Path
	}
	_, _ = fmt.Fprintf(h, "%s\n%s\n", r.Method, route)
}

// readBodyPrefix reads at most fingerprintBodyLimit+1 bytes of the request body
// and restores the body for the handler. The second result reports whether the
// whole body fit in the limit.
func readBodyPrefix(r *http.Request) ([]byte, bool, error) {
	if r.Body == nil {
		return nil, true, nil
	}

	prefix, err := io.ReadAll(io.LimitReader(r.Body, fingerprintBodyLimit+1))
	if err != nil {
		return nil, false, err
	}
	if len(prefix) > fingerprintBodyLimit {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), r.Body))
		return prefix, false, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(prefix))
	return prefix, true, nil
}

// multipartParts parses a multipart body and returns its canonical parts as
// sorted strings. A text field is "field\x00name\x00value"; a file part is
// "file\x00name\x00filename\x00content-type\x00sha256". Sorting makes the
// fingerprint independent of the field and part order.
func multipartParts(boundary string, body io.Reader) ([]string, error) {
	reader := multipart.NewReader(body, boundary)
	parts := []string{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		if part.FileName() != "" {
			sum := sha256.New()
			if _, err := io.Copy(sum, part); err != nil {
				return nil, err
			}
			parts = append(parts, "file\x00"+part.FormName()+"\x00"+part.FileName()+
				"\x00"+part.Header.Get("Content-Type")+"\x00"+hex.EncodeToString(sum.Sum(nil)))
			continue
		}

		value, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "field\x00"+part.FormName()+"\x00"+string(value))
	}
	slices.Sort(parts)
	return parts, nil
}
