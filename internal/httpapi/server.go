// Package httpapi implements the wzap REST API: the server wiring, the
// response envelope and the shared middleware chain.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"wzap/internal/config"
	"wzap/internal/storage"
)

// ReadyChecker reports whether the service dependencies are ready to serve
// traffic.
type ReadyChecker interface {
	Check(ctx context.Context) error
}

// Deps carries the dependencies consumed by HTTP handlers. It grows as later
// tasks register handlers.
type Deps struct {
	ReadyChecker ReadyChecker
	Instances    InstanceService
	Numbers      NumberResolver
	Messages     MessageService
	Idempotency  storage.IdempotencyRepository
	Media        MediaStore
	Users        storage.UserRepository
	Keys         storage.APIKeyRepository
	JWTSecret    string
}

// New builds the HTTP server with the middleware chain, the exact public
// health endpoints and the authenticated API sub-mux mounted at /.
//
// The only exact publics are GET /healthz and GET /readyz. /swagger/ and
// /manager/ (plus static assets) stay unregistered until tasks 5.2 and 6.6
// own them, so no stub is mounted for them here.
func New(cfg config.Config, log *slog.Logger, deps Deps) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(deps.ReadyChecker, log))

	// The legacy /api/v1 prefix is gone: every path under it answers the
	// shared 404 envelope, with or without credential. This subtree pattern
	// is more specific than "/" below, so it wins for legacy paths.
	mux.Handle("/api/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Error(w, r, http.StatusNotFound, "not_found", "route not found")
	}))

	api := http.NewServeMux()
	api.HandleFunc("POST /instances", handleCreateInstance(deps.Instances))
	api.HandleFunc("GET /instances", handleListInstances(deps.Instances))
	api.HandleFunc("GET /instances/{id}", handleGetInstance(deps.Instances))
	api.HandleFunc("PATCH /instances/{id}", handleUpdateInstance(deps.Instances))
	api.HandleFunc("DELETE /instances/{id}", handleDeleteInstance(deps.Instances))
	api.HandleFunc("POST /instances/{id}/connect", handleConnectInstance(deps.Instances))
	api.HandleFunc("POST /instances/{id}/disconnect", handleDisconnectInstance(deps.Instances))
	api.HandleFunc("GET /instances/{id}/qr", handleQRInstance(deps.Instances))
	api.HandleFunc("GET /instances/{id}/status", handleInstanceStatus(deps.Instances))
	api.HandleFunc("POST /instances/{id}/numbers/check", handleCheckNumber(deps.Instances, deps.Numbers))
	api.Handle("POST /instances/{id}/messages/text", Idempotency(deps.Idempotency, log, cfg.MaxMediaBytes)(handleSendText(deps.Instances, deps.Messages)))
	api.Handle("POST /instances/{id}/messages/location", Idempotency(deps.Idempotency, log, cfg.MaxMediaBytes)(handleSendLocation(deps.Instances, deps.Messages)))
	api.Handle("POST /instances/{id}/messages/contact", Idempotency(deps.Idempotency, log, cfg.MaxMediaBytes)(handleSendContact(deps.Instances, deps.Messages)))
	api.Handle("POST /instances/{id}/messages/media", Idempotency(deps.Idempotency, log, cfg.MaxMediaBytes)(handleSendMedia(deps.Instances, deps.Messages, deps.Media, cfg.MaxMediaBytes)))
	api.HandleFunc("GET /instances/{id}/messages", handleListMessages(deps.Instances, deps.Messages))
	api.HandleFunc("GET /instances/{id}/messages/{message_id}", handleGetMessage(deps.Instances, deps.Messages))
	api.HandleFunc("GET /media/{id}", handleGetMedia(deps.Instances, deps.Media))
	// "/" is the least-specific outer pattern, so Authenticate runs before
	// the api mux sees the request: an unknown path without credential
	// answers 401 here, while the same path with a valid credential falls
	// through to the enveloped 404 of envelopeFallback.
	mux.Handle("/", Authenticate(cfg.APIKey, deps.Users, deps.Keys, deps.JWTSecret)(envelopeFallback(api)))

	// The session endpoints authenticate with the cookie, never with the
	// apikey header, so they mount on the outer mux outside the Authenticate
	// guard at their prefix-less paths.
	secure := secureCookies(cfg.PublicURL)
	authMux := http.NewServeMux()
	authMux.HandleFunc("POST /auth/login", handleLogin(deps.Users, deps.JWTSecret, secure))
	authMux.HandleFunc("POST /auth/logout", handleLogout(secure))
	authMux.HandleFunc("GET /auth/me", handleMe(deps.Users, deps.JWTSecret))
	mux.Handle("/auth/", envelopeFallback(authMux))

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           RequestID(Logging(log)(Recover(log)(mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// envelopeFallback turns the plain-text 404 and 405 responses of the API mux
// into the shared error envelope. A request whose path is registered with other
// methods answers 405 with the Allow header naming them; every other unrouted
// request answers 404.
func envelopeFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		if allowed := allowedMethods(mux, r); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		Error(w, r, http.StatusNotFound, "not_found", "route not found")
	})
}

// allowedMethods reports the methods the mux registers for the path of r. The
// mux does not expose its route table, so each method is probed in turn; a
// method-less request never reaches this helper.
func allowedMethods(mux *http.ServeMux, r *http.Request) []string {
	var allowed []string
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := mux.Handler(probe); pattern != "" {
			allowed = append(allowed, method)
		}
	}
	return allowed
}
