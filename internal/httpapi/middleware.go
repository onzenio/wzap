package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
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

// Auth guards handlers with the service token compared in constant time.
func Auth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !validBearer(r.Header.Get("Authorization"), token) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				Error(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid service token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validBearer(header, token string) bool {
	if token == "" {
		return false
	}
	scheme, credentials, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(credentials), []byte(token)) == 1
}
