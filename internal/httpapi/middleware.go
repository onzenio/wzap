package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/storage"
)

const requestIDHeader = "X-Request-Id"

type contextKey int

const requestIDKey contextKey = iota

// RequestIDFromContext returns the request id attached by RequestID, or an
// empty string when the request did not pass through it.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID echoes a caller supplied X-Request-Id or generates one, stores it
// in the request context and returns it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// Logging emits one structured log line per request with the correlation id.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.InfoContext(r.Context(), "http request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// statusRecorder captures the status code written by the inner handler so
// Logging can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Recover converts a handler panic into a 500 error envelope. The panic value
// and stack are logged server-side and never written to the client.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				log.ErrorContext(r.Context(), "panic recovered",
					"request_id", RequestIDFromContext(r.Context()),
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Authenticate guards handlers with one credential per request, resolved in a
// fixed order with first hit winning: the wzap_session cookie validated with
// auth.ParseToken, the apikey header equal to the global key, then the hex
// sha256 of the apikey header looked up in the key repository. An unusable
// cookie (absent, unparseable, expired, wrong secret) counts as absent and
// falls through to the apikey steps; only when no step resolves does the
// middleware fail with 401. The legacy Authorization: Bearer scheme is never
// read. The resolved auth.Scope is injected in the request context for
// downstream handlers via auth.ContextWithScope.
func Authenticate(globalKey string, users storage.UserRepository, keys storage.APIKeyRepository, jwtSecret string) func(http.Handler) http.Handler {
	// Session validity is JWT-only: an unusable cookie falls through per R12
	// and user-to-instance ownership stays in the handlers (2.4), so the
	// users repository is intentionally unused here.
	_ = users
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
				if scope, err := auth.ParseToken(cookie.Value, jwtSecret); err == nil {
					next.ServeHTTP(w, r.WithContext(auth.ContextWithScope(r.Context(), scope)))
					return
				}
			}

			if presented := r.Header.Get("apikey"); presented != "" {
				if globalKey != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(globalKey)) == 1 {
					next.ServeHTTP(w, r.WithContext(auth.ContextWithScope(r.Context(), auth.Scope{Kind: auth.ScopeGlobal})))
					return
				}
				if keys != nil {
					sum := sha256.Sum256([]byte(presented))
					id, err := keys.InstanceByHash(r.Context(), hex.EncodeToString(sum[:]))
					switch {
					case err == nil:
						next.ServeHTTP(w, r.WithContext(auth.ContextWithScope(r.Context(), auth.Scope{
							Kind:       auth.ScopeInstance,
							InstanceID: id,
						})))
						return
					case errors.Is(err, storage.ErrNotFound):
						// Unknown key: fall through to the shared 401 below.
					default:
						Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
						return
					}
				}
			}

			Error(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid credential")
		})
	}
}
