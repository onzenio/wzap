package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"wzap/internal/chatwoot/client"
	"wzap/internal/chatwoot/config"
	"wzap/internal/chatwoot/inbound"
	cfgpkg "wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// ChatwootConfigStore persists the per-instance Chatwoot connector config.
type ChatwootConfigStore interface {
	Get(ctx context.Context, instanceID uuid.UUID) (*model.ChatwootConfig, error)
	Put(ctx context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error)
	Delete(ctx context.Context, instanceID uuid.UUID) error
}

// ChatwootInbound processes the open webhook payload.
type ChatwootInbound interface {
	Handle(ctx context.Context, instanceID uuid.UUID, payload inbound.Payload) (int, error)
	HandleCommand(ctx context.Context, instanceID uuid.UUID, command string, conversationID int64) (int, error)
}

// ChatwootInboxClient provisions the api inbox for auto_create.
type ChatwootInboxClient interface {
	ListInboxes(ctx context.Context) ([]client.Inbox, error)
	CreateInbox(ctx context.Context, req client.CreateInboxRequest) (*client.Inbox, error)
}

// ChatwootClientFor builds the per-instance Chatwoot API for auto_create.
// Nil means provisioning is skipped (tests); production wires client.New.
type ChatwootClientFor func(cfg model.ChatwootConfig) ChatwootInboxClient

// The repositories satisfy the handler contracts; the assertions catch
// signature drift at build time.
var (
	_ ChatwootConfigStore = (storage.ChatwootConfigRepository)(nil)
	_ ChatwootInbound     = (*inbound.Handler)(nil)
)

// ChatwootImporter runs the manual history import of an instance, returning
// how many messages were written. Production wires the chatimport run;
// tests replay a count.
type ChatwootImporter interface {
	ImportHistory(ctx context.Context, instanceID uuid.UUID) (int, error)
}

// chatwootSetRequest is the PUT /instances/{id}/chatwoot payload. Booleans
// are values (absent means false); sign_msg type errors are mapped to 422 by
// inspecting the decode failure.
type chatwootSetRequest struct {
	Enabled             bool     `json:"enabled"`
	URL                 string   `json:"url"`
	AccountID           string   `json:"account_id"`
	Token               string   `json:"token"`
	NameInbox           string   `json:"name_inbox"`
	SignMsg             bool     `json:"sign_msg"`
	SignDelimiter       string   `json:"sign_delimiter"`
	ReopenConversation  bool     `json:"reopen_conversation"`
	ConversationPending bool     `json:"conversation_pending"`
	MergeBrazilContacts bool     `json:"merge_brazil_contacts"`
	ImportContacts      bool     `json:"import_contacts"`
	ImportMessages      bool     `json:"import_messages"`
	DaysLimit           int      `json:"days_limit"`
	AutoCreate          bool     `json:"auto_create"`
	Organization        string   `json:"organization"`
	Logo                string   `json:"logo"`
	IgnoreJIDs          []string `json:"ignore_jids"`
}

// chatwootConfigResponse is the GET/PUT /instances/{id}/chatwoot body: the
// stored connector plus the computed webhook_url to register in Chatwoot.
type chatwootConfigResponse struct {
	InstanceID          string   `json:"instance_id"`
	Enabled             bool     `json:"enabled"`
	URL                 string   `json:"url"`
	AccountID           string   `json:"account_id"`
	Token               string   `json:"token"`
	NameInbox           string   `json:"name_inbox"`
	SignMsg             bool     `json:"sign_msg"`
	SignDelimiter       string   `json:"sign_delimiter"`
	ReopenConversation  bool     `json:"reopen_conversation"`
	ConversationPending bool     `json:"conversation_pending"`
	MergeBrazilContacts bool     `json:"merge_brazil_contacts"`
	ImportContacts      bool     `json:"import_contacts"`
	ImportMessages      bool     `json:"import_messages"`
	DaysLimit           int      `json:"days_limit"`
	AutoCreate          bool     `json:"auto_create"`
	Organization        string   `json:"organization"`
	Logo                string   `json:"logo"`
	IgnoreJIDs          []string `json:"ignore_jids"`
	WebhookURL          string   `json:"webhook_url"`
}

// handleChatwootSet stores the connector config behind the dual auth. The
// global gate answers 400 when disabled; validation failures answer 422
// without persisting.
func handleChatwootSet(instances InstanceService, configs ChatwootConfigStore, global cfgpkg.Chatwoot, publicURL string, clientFor ChatwootClientFor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !global.Enabled {
			Error(w, r, http.StatusBadRequest, "chatwoot_disabled", "chatwoot connector is disabled")
			return
		}
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}
		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}
		var request chatwootSetRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid chatwoot config: "+typeErr.Field+" has wrong type")
				return
			}
			writeJSONBodyError(w, r, err)
			return
		}
		nameInbox := strings.TrimSpace(request.NameInbox)
		if nameInbox == "" {
			nameInbox = config.DefaultInbox(stored.Name)
		}
		delimiter := request.SignDelimiter
		if delimiter == "" {
			delimiter = config.DefaultDelimiter()
		}
		ignoreJIDs := request.IgnoreJIDs
		if ignoreJIDs == nil {
			ignoreJIDs = []string{}
		}
		cfg := model.ChatwootConfig{
			InstanceID:          id,
			Enabled:             request.Enabled,
			URL:                 strings.TrimSpace(request.URL),
			AccountID:           strings.TrimSpace(request.AccountID),
			Token:               request.Token,
			NameInbox:           nameInbox,
			SignMsg:             request.SignMsg,
			SignDelimiter:       delimiter,
			ReopenConversation:  request.ReopenConversation,
			ConversationPending: request.ConversationPending,
			MergeBrazilContacts: request.MergeBrazilContacts,
			ImportContacts:      request.ImportContacts,
			ImportMessages:      request.ImportMessages,
			DaysLimit:           request.DaysLimit,
			AutoCreate:          request.AutoCreate,
			Organization:        strings.TrimSpace(request.Organization),
			Logo:                strings.TrimSpace(request.Logo),
			IgnoreJIDs:          ignoreJIDs,
		}
		if err := config.Validate(cfg); err != nil {
			var field *config.ErrField
			if errors.As(err, &field) {
				Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid chatwoot config: "+field.Field+" "+field.Message)
				return
			}
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid chatwoot config")
			return
		}
		saved, err := configs.Put(r.Context(), cfg)
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		webhookURL := chatwootWebhookURL(publicURL, id)
		if saved.Enabled && saved.AutoCreate && clientFor != nil {
			ensureChatwootInbox(r.Context(), clientFor, *saved, webhookURL)
		}
		JSON(w, r, http.StatusOK, newChatwootConfigResponse(saved, webhookURL))
	}
}

// ensureChatwootInbox guarantees the api inbox for auto_create, reusing it
// by name. Best-effort: failures only warn, never fail the set.
func ensureChatwootInbox(ctx context.Context, clientFor ChatwootClientFor, cfg model.ChatwootConfig, webhookURL string) {
	cli := clientFor(cfg)
	if cli == nil {
		return
	}
	inboxes, err := cli.ListInboxes(ctx)
	if err != nil {
		slog.Warn("chatwoot auto_create inbox listing failed", "instance_id", cfg.InstanceID, "error", err)
		return
	}
	for _, inbox := range inboxes {
		if inbox.Name == cfg.NameInbox {
			return
		}
	}
	if _, err := cli.CreateInbox(ctx, client.CreateInboxRequest{Name: cfg.NameInbox, WebhookURL: webhookURL}); err != nil {
		slog.Warn("chatwoot auto_create inbox creation failed", "instance_id", cfg.InstanceID, "error", err)
	}
}

// handleChatwootGet returns the connector config behind the dual auth. An
// instance that was never configured answers 200 disabled with empty fields.
func handleChatwootGet(instances InstanceService, configs ChatwootConfigStore, global cfgpkg.Chatwoot, publicURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !global.Enabled {
			Error(w, r, http.StatusBadRequest, "chatwoot_disabled", "chatwoot connector is disabled")
			return
		}
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}
		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}
		cfg, err := configs.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				JSON(w, r, http.StatusOK, newChatwootConfigResponse(&model.ChatwootConfig{InstanceID: id, IgnoreJIDs: []string{}}, chatwootWebhookURL(publicURL, id)))
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		JSON(w, r, http.StatusOK, newChatwootConfigResponse(cfg, chatwootWebhookURL(publicURL, id)))
	}
}

// handleChatwootWebhook processes POST /chatwoot/webhook/{id} outside auth:
// open by design, the secret is v2. The global gate answers 400 when
// disabled; discards answer 200 with a bot body. O limiter responde 429
// no estouro por instância; comandos operacionais nunca executam aqui
// (descartam 200 no inbound) — usam POST /instances/{id}/chatwoot/command.
func handleChatwootWebhook(instances InstanceService, inb ChatwootInbound, global cfgpkg.Chatwoot, limiter *ChatwootRateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id, err := uuid.Parse(rawID)
		if err != nil {
			Error(w, r, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		if limiter != nil && !limiter.Allow(id) {
			Error(w, r, http.StatusTooManyRequests, "rate_limited", "rate limited")
			return
		}
		if _, err := instances.Get(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if !global.Enabled {
			Error(w, r, http.StatusBadRequest, "chatwoot_disabled", "chatwoot connector is disabled")
			return
		}
		if inb == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		var payload inbound.Payload
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			Error(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		status, err := inb.Handle(r.Context(), id, payload)
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if status != http.StatusOK {
			code, message := chatwootWebhookError(status)
			Error(w, r, status, code, message)
			return
		}
		writeChatwootBotBody(w)
	}
}

// chatwootWebhookError maps a non-200 inbound status to its error code.
// chatwoot_disabled is reserved for the 400 global-off gate; every other
// status keeps its own code so callers can tell a disabled connector apart
// from a missing instance or an internal failure.
func chatwootWebhookError(status int) (code, message string) {
	switch status {
	case http.StatusBadRequest:
		return "chatwoot_disabled", "chatwoot connector is disabled"
	case http.StatusNotFound:
		return "not_found", "instance not found"
	case http.StatusTooManyRequests:
		return "rate_limited", "rate limited"
	case http.StatusInternalServerError:
		return "internal_error", "internal server error"
	default:
		return "chatwoot_error", "chatwoot webhook failed"
	}
}

// writeChatwootBotBody answers the open webhook with the bot body Chatwoot
// expects: 200 with a JSON content envelope, empty when nothing was done.
func writeChatwootBotBody(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"content": ""})
}

// newChatwootConfigResponse maps a stored config plus its webhook URL. The
// token is accepted on write only and never echoed back: responses always
// carry an empty token so a read cannot leak the Chatwoot credential.
func newChatwootConfigResponse(cfg *model.ChatwootConfig, webhookURL string) chatwootConfigResponse {
	ignoreJIDs := cfg.IgnoreJIDs
	if ignoreJIDs == nil {
		ignoreJIDs = []string{}
	}
	return chatwootConfigResponse{
		InstanceID:          cfg.InstanceID.String(),
		Enabled:             cfg.Enabled,
		URL:                 cfg.URL,
		AccountID:           cfg.AccountID,
		Token:               "",
		NameInbox:           cfg.NameInbox,
		SignMsg:             cfg.SignMsg,
		SignDelimiter:       cfg.SignDelimiter,
		ReopenConversation:  cfg.ReopenConversation,
		ConversationPending: cfg.ConversationPending,
		MergeBrazilContacts: cfg.MergeBrazilContacts,
		ImportContacts:      cfg.ImportContacts,
		ImportMessages:      cfg.ImportMessages,
		DaysLimit:           cfg.DaysLimit,
		AutoCreate:          cfg.AutoCreate,
		Organization:        cfg.Organization,
		Logo:                cfg.Logo,
		IgnoreJIDs:          ignoreJIDs,
		WebhookURL:          webhookURL,
	}
}

// handleChatwootImport runs the manual history import behind the dual auth.
// The global gate answers 400 when disabled; an unconfigured instance
// answers 404; success answers 202 with the imported message count.
func handleChatwootImport(instances InstanceService, configs ChatwootConfigStore, global cfgpkg.Chatwoot, importer ChatwootImporter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !global.Enabled {
			Error(w, r, http.StatusBadRequest, "chatwoot_disabled", "chatwoot connector is disabled")
			return
		}
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}
		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}
		if _, err := configs.Get(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				Error(w, r, http.StatusNotFound, "not_found", "chatwoot not configured")
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if importer == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		imported, err := importer.ImportHistory(r.Context(), id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				Error(w, r, http.StatusNotFound, "not_found", "chatwoot not configured")
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		JSON(w, r, http.StatusAccepted, map[string]any{"imported": imported})
	}
}

// chatwootCommandRequest is the POST /instances/{id}/chatwoot/command payload.
type chatwootCommandRequest struct {
	Command        string `json:"command"`
	ConversationID int64  `json:"conversation_id"`
}

// handleChatwootCommand runs operational commands behind the dual auth
// (status, init[:number], clearcache, disconnect). O webhook aberto nunca
// executa comandos; esta rota autenticada é o único caminho.
func handleChatwootCommand(instances InstanceService, configs ChatwootConfigStore, global cfgpkg.Chatwoot, inb ChatwootInbound) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !global.Enabled {
			Error(w, r, http.StatusBadRequest, "chatwoot_disabled", "chatwoot connector is disabled")
			return
		}
		id, ok := instanceID(w, r)
		if !ok {
			return
		}
		if denyForeignInstanceKey(w, r, id) {
			return
		}
		stored, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, stored); err != nil {
			writeForbidden(w, r)
			return
		}
		if _, err := configs.Get(r.Context(), id); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				Error(w, r, http.StatusNotFound, "not_found", "chatwoot not configured")
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		var request chatwootCommandRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}
		if strings.TrimSpace(request.Command) == "" {
			Error(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		if inb == nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		status, err := inb.HandleCommand(r.Context(), id, request.Command, request.ConversationID)
		if err != nil {
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if status != http.StatusOK {
			code, message := chatwootWebhookError(status)
			Error(w, r, status, code, message)
			return
		}
		JSON(w, r, http.StatusOK, map[string]any{"ok": true})
	}
}

// chatwootWebhookURL builds the webhook URL to register in Chatwoot: the
// public base plus the open route, or the relative route without a base.
func chatwootWebhookURL(publicURL string, instanceID uuid.UUID) string {
	path := "/chatwoot/webhook/" + instanceID.String()
	base := strings.TrimSuffix(strings.TrimSpace(publicURL), "/")
	if base == "" {
		return path
	}
	return base + path
}
