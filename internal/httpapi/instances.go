package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/instance"
	"wzap/internal/model"
	"wzap/internal/storage"
	"wzap/internal/webhook"
)

const (
	// defaultInstancesLimit is the page size used when the request omits limit.
	defaultInstancesLimit = 50
	// maxInstancesLimit caps the page size a client can request.
	maxInstancesLimit = 100
	// maxJSONBodyBytes caps the JSON request bodies every handler decodes.
	maxJSONBodyBytes = 1 << 20
)

// InstanceService is the instance management contract consumed by the handlers.
type InstanceService interface {
	Create(ctx context.Context, input instance.CreateInput) (*model.Instance, string, error)
	// OldestAdmin resolves the owner of an instance created by the global key
	// or an admin session without an explicit owner.
	OldestAdmin(ctx context.Context) (uuid.UUID, error)
	Get(ctx context.Context, id uuid.UUID) (*model.Instance, error)
	List(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error)
	Update(ctx context.Context, id uuid.UUID, input instance.UpdateInput) (*model.Instance, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Disconnect(ctx context.Context, id uuid.UUID) error
	Connect(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error)
	QR(ctx context.Context, id uuid.UUID) (instance.ConnectResult, error)
}

// The service satisfies the handler contract; the assertion catches signature
// drift at build time.
var _ InstanceService = (*instance.Service)(nil)

// instanceResponse is the JSON representation of an instance. It carries the
// owner and the webhook configuration on every read but never the instance
// API key: the key is returned in clear exactly once by the create response
// below. A null webhook_url means no webhook is configured.
type instanceResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	ExternalRef     string     `json:"external_ref"`
	OwnerUserID     *uuid.UUID `json:"owner_user_id"`
	WebhookURL      *string    `json:"webhook_url"`
	WebhookEnabled  bool       `json:"webhook_enabled"`
	WebhookEvents   []string   `json:"webhook_events"`
	Status          string     `json:"status"`
	WhatsAppJID     string     `json:"whatsapp_jid"`
	LastError       string     `json:"last_error"`
	LastConnectedAt *time.Time `json:"last_connected_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// createInstanceResponse is the 201 answer to a creation: the instance with
// its one-time plaintext key. No other route returns this shape.
type createInstanceResponse struct {
	instanceResponse
	InstanceAPIKey string `json:"instance_api_key"`
}

// instanceListResponse is the JSON representation of an instance page.
type instanceListResponse struct {
	Items      []instanceResponse `json:"items"`
	NextCursor string             `json:"next_cursor"`
}

// createInstanceRequest is the POST /instances payload. OwnerUserID is a
// pointer so an absent field is distinct from an explicit value: only the
// global scope and admin sessions may send it. The webhook fields follow the
// same absent-versus-explicit rule: omitted events default to every type,
// while an omitted URL stays unset.
type createInstanceRequest struct {
	Name           string    `json:"name"`
	ExternalRef    string    `json:"external_ref"`
	OwnerUserID    *string   `json:"owner_user_id"`
	WebhookURL     *string   `json:"webhook_url"`
	WebhookEnabled *bool     `json:"webhook_enabled"`
	WebhookEvents  *[]string `json:"webhook_events"`
}

// updateInstanceRequest uses pointers so an omitted field keeps its stored
// value while an explicit empty external_ref clears it. The webhook fields
// behave the same: omitted keeps the stored configuration, an explicit empty
// URL unsets it, and an explicit empty events list clears the subscription.
type updateInstanceRequest struct {
	Name           *string   `json:"name"`
	ExternalRef    *string   `json:"external_ref"`
	WebhookURL     *string   `json:"webhook_url"`
	WebhookEnabled *bool     `json:"webhook_enabled"`
	WebhookEvents  *[]string `json:"webhook_events"`
}

// handleCreateInstance registers an instance, assigns its owner and answers
// 201 with the instance plus its one-time plaintext key. The collection is
// outside every instance key scope, so instance credentials answer 403 before
// the body is read. Owner resolution is by scope: a user session always owns
// what it creates (sending owner_user_id is an admin/global privilege and
// answers 403); the global scope and admin sessions create for the requested
// owner_user_id when given and for the oldest admin otherwise (500 when no
// admin exists). An override matching no user answers 422. After owner
// resolution and before Create, a non-admin user session faces the quota
// pre-checks: the global ceiling (CountAll >= MaxInstances when MaxInstances
// is positive) then the owner's stored per-user quota (CountByOwner >=
// quota when the stored quota is positive; a stored 0 means unlimited and
// WZAP_DEFAULT_USER_INSTANCE_QUOTA is never consulted here). Either breach
// answers 403 quota_exceeded. The global scope and admin sessions bypass both
// checks. The check-then-insert race is accepted at product scale (R19): two
// concurrent creates may both pass and both insert; no locking is built.
//
// @Summary Create an instance
// @Tags instances
// @Accept json
// @Produce json
// @Security apikey
// @Param apikey header string true "Global key or user session credential scope"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param request body createInstanceRequest true "Instance payload"
// @Success 201 {object} createInstanceResponse "Created, wrapped in the data envelope"
// @Failure 400 {object} errorEnvelope "Malformed body"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Forbidden or quota exceeded"
// @Failure 409 {object} errorEnvelope "External ref already taken"
// @Failure 413 {object} errorEnvelope "Body exceeds the 1 MiB limit"
// @Failure 422 {object} errorEnvelope "Unknown owner or invalid webhook config"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances [post]
func handleCreateInstance(instances InstanceService, users storage.UserRepository, keys storage.APIKeyRepository, maxInstances int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeCollection(r); err != nil {
			writeForbidden(w, r)
			return
		}

		var request createInstanceRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		scope, ok := auth.ScopeFromContext(r.Context())
		if !ok {
			writeForbidden(w, r)
			return
		}

		var owner uuid.UUID
		if err := auth.RequireRole(scope, "admin"); err == nil {
			if request.OwnerUserID != nil {
				id, err := uuid.Parse(*request.OwnerUserID)
				if err != nil {
					Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid owner_user_id")
					return
				}
				owner = id
			} else {
				admin, err := instances.OldestAdmin(r.Context())
				if err != nil {
					if errors.Is(err, instance.ErrNoAdmin) {
						Error(w, r, http.StatusInternalServerError, "internal_error", "no admin user exists")
						return
					}
					Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
					return
				}
				owner = admin
			}
		} else if scope.Kind == auth.ScopeUser {
			if request.OwnerUserID != nil {
				writeForbidden(w, r)
				return
			}
			owner = scope.UserID
		} else {
			writeForbidden(w, r)
			return
		}

		if err := checkCreateQuotas(r, scope, owner, users, keys, maxInstances); err != nil {
			writeQuotaError(w, r, err)
			return
		}

		created, key, err := instances.Create(r.Context(), instance.CreateInput{
			Name:           request.Name,
			ExternalRef:    request.ExternalRef,
			OwnerUserID:    &owner,
			WebhookURL:     request.WebhookURL,
			WebhookEnabled: request.WebhookEnabled,
			WebhookEvents:  request.WebhookEvents,
		})
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		webhook.Keys.Store(created.ID, key)
		JSON(w, http.StatusCreated, newCreateInstanceResponse(created, key))
	}
}

// errQuotaExceeded reports that a create-time quota check denied the request.
// The handler maps it to 403 quota_exceeded.
var errQuotaExceeded = errors.New("quota exceeded")

// checkCreateQuotas enforces the global and per-user instance quotas for a
// non-admin user session creating for owner. Admin-equivalent creators (the
// global scope or an admin session) skip both checks entirely. The counts are
// unfiltered: every existing instance counts, any state. A non-positive
// MaxInstances disables the global check and a stored per-user quota of 0
// means unlimited for that check; the creation-time default quota is never
// consulted here. The checks are fail-closed: a nil keys or users dependency
// is a wiring error that answers 500 instead of silently disabling
// enforcement (production always wires both).
func checkCreateQuotas(r *http.Request, scope auth.Scope, owner uuid.UUID, users storage.UserRepository, keys storage.APIKeyRepository, maxInstances int) error {
	if err := auth.RequireRole(scope, "admin"); err == nil {
		return nil
	}
	if scope.Kind != auth.ScopeUser {
		return nil
	}

	if keys == nil {
		return fmt.Errorf("check create quotas: keys repository is not configured")
	}
	if maxInstances > 0 {
		total, err := keys.CountAll(r.Context())
		if err != nil {
			return err
		}
		if total >= maxInstances {
			return errQuotaExceeded
		}
	}

	if users == nil {
		return fmt.Errorf("check create quotas: users repository is not configured")
	}
	ownerUser, err := users.GetByID(r.Context(), owner)
	if err != nil {
		// An unknown owner stays the service's 422: skip the per-user check
		// and let Create validate the owner.
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	if ownerUser.InstanceQuota <= 0 {
		return nil
	}
	owned, err := keys.CountByOwner(r.Context(), owner)
	if err != nil {
		return err
	}
	if owned >= ownerUser.InstanceQuota {
		return errQuotaExceeded
	}
	return nil
}

// writeQuotaError maps a quota pre-check failure to its HTTP status: a breach
// answers 403 quota_exceeded, anything else a 500 without leaking its cause.
func writeQuotaError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errQuotaExceeded) {
		Error(w, r, http.StatusForbidden, "quota_exceeded", "quota exceeded")
		return
	}
	Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
}

// handleListInstances answers one page of instances with its next cursor. An
// instance key owns no collection view and answers 403; a user session sees
// exactly its own rows while the global scope and admin sessions see all.
//
// @Summary List instances
// @Tags instances
// @Produce json
// @Security apikey
// @Param apikey header string true "Global key or user session credential scope"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param limit query int false "Page size, default 50, max 100"
// @Param cursor query string false "Opaque pagination cursor"
// @Success 200 {object} instanceListResponse "One page, wrapped in the data envelope"
// @Failure 400 {object} errorEnvelope "Invalid cursor"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Instance keys own no collection view"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances [get]
func handleListInstances(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeCollection(r); err != nil {
			writeForbidden(w, r)
			return
		}

		items, next, err := instances.List(r.Context(),
			parseInstancesLimit(r.URL.Query().Get("limit")), r.URL.Query().Get("cursor"))
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}

		if scope, ok := auth.ScopeFromContext(r.Context()); ok {
			items = filterInstancesByOwner(scope, items)
		} else {
			items = nil
		}

		response := instanceListResponse{Items: make([]instanceResponse, 0, len(items)), NextCursor: next}
		for i := range items {
			response.Items = append(response.Items, newInstanceResponse(&items[i]))
		}
		JSON(w, http.StatusOK, response)
	}
}

// handleGetInstance answers one instance by id. The target loads first so a
// missing id answers 404 before the ownership check can deny with 403.
//
// @Summary Get an instance
// @Tags instances
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 200 {object} instanceResponse "Instance, wrapped in the data envelope"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id} [get]
func handleGetInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		found, err := instances.Get(r.Context(), id)
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		if err := authorizeInstance(r, found); err != nil {
			writeForbidden(w, r)
			return
		}
		JSON(w, http.StatusOK, newInstanceResponse(found))
	}
}

// handleUpdateInstance applies a partial update and answers 200 with the stored
// instance. It loads the target first (404) and authorizes (403) before
// reading the body or writing anything.
//
// @Summary Update an instance
// @Tags instances
// @Accept json
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Param request body updateInstanceRequest true "Partial update payload"
// @Success 200 {object} instanceResponse "Updated instance, wrapped in the data envelope"
// @Failure 400 {object} errorEnvelope "Malformed body or invalid cursor"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 409 {object} errorEnvelope "External ref already taken"
// @Failure 413 {object} errorEnvelope "Body exceeds the 1 MiB limit"
// @Failure 422 {object} errorEnvelope "Invalid webhook config"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id} [patch]
func handleUpdateInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
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

		var request updateInstanceRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		updated, err := instances.Update(r.Context(), id, instance.UpdateInput{
			Name:           request.Name,
			ExternalRef:    request.ExternalRef,
			WebhookURL:     request.WebhookURL,
			WebhookEnabled: request.WebhookEnabled,
			WebhookEvents:  request.WebhookEvents,
		})
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, newInstanceResponse(updated))
	}
}

// handleDeleteInstance removes an instance and answers 204. It loads the
// target first (404) and authorizes (403) before removing anything.
//
// @Summary Delete an instance
// @Tags instances
// @Produce json
// @Security apikey
// @Param apikey header string true "Global, owning user, or own instance key"
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Param id path string true "Instance ID (UUID)"
// @Success 204 "Deleted, no body"
// @Failure 401 {object} errorEnvelope "Missing or invalid credential"
// @Failure 403 {object} errorEnvelope "Not the owner"
// @Failure 404 {object} errorEnvelope "Instance not found"
// @Failure 500 {object} errorEnvelope "Internal error"
// @Router /instances/{id} [delete]
func handleDeleteInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
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

		if err := instances.Delete(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}
		webhook.Keys.Clear(id)
		JSON(w, http.StatusNoContent, nil)
	}
}

// instanceID parses the {id} path value. A malformed id answers 404: a value
// that is not a UUID names no instance.
func instanceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
		return uuid.Nil, false
	}
	return id, true
}

// parseInstancesLimit reads the limit query parameter with the instance
// defaults.
func parseInstancesLimit(raw string) int {
	return parseLimit(raw, defaultInstancesLimit, maxInstancesLimit)
}

// parseLimit reads a limit query parameter, falling back to fallback when it is
// missing or malformed and capping the page size at maxLimit.
func parseLimit(raw string, fallback, maxLimit int) int {
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	return min(limit, maxLimit)
}

// decodeJSONBody decodes the request body into target, refusing bodies above
// maxJSONBodyBytes before they are buffered.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	return json.NewDecoder(r.Body).Decode(target)
}

// writeJSONBodyError maps a body decoding failure to its HTTP status: an
// oversized body answers 413 and anything else a malformed 400.
func writeJSONBodyError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large",
			"request body exceeds the 1 MiB limit")
		return
	}
	Error(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
}

// newInstanceResponse maps a stored instance to its JSON representation. A nil
// stored events list is emitted as an empty array so the field keeps its array
// shape on every read.
func newInstanceResponse(inst *model.Instance) instanceResponse {
	events := inst.WebhookEvents
	if events == nil {
		events = []string{}
	}
	return instanceResponse{
		ID:              inst.ID.String(),
		Name:            inst.Name,
		ExternalRef:     inst.ExternalRef,
		OwnerUserID:     inst.OwnerUserID,
		WebhookURL:      inst.WebhookURL,
		WebhookEnabled:  inst.WebhookEnabled,
		WebhookEvents:   events,
		Status:          inst.Status,
		WhatsAppJID:     inst.WhatsAppJID,
		LastError:       inst.LastError,
		LastConnectedAt: inst.LastConnectedAt,
		CreatedAt:       inst.CreatedAt,
		UpdatedAt:       inst.UpdatedAt,
	}
}

// newCreateInstanceResponse maps a created instance and its one-time plaintext
// key to the 201 body. The key travels in this response only.
func newCreateInstanceResponse(inst *model.Instance, key string) createInstanceResponse {
	return createInstanceResponse{
		instanceResponse: newInstanceResponse(inst),
		InstanceAPIKey:   key,
	}
}

// writeInstanceError maps a service error to its HTTP status and error
// envelope. Scope denials answer 403, unknown owner overrides and invalid
// webhook configurations answer 422, a missing oldest admin answers 500 with
// a clear server-state message; unknown failures answer 500 without leaking
// their cause.
func writeInstanceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		Error(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, instance.ErrNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
	case errors.Is(err, instance.ErrExternalRefTaken):
		Error(w, r, http.StatusConflict, "conflict", "external ref already taken")
	case errors.Is(err, instance.ErrInvalidCursor):
		Error(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
	case errors.Is(err, instance.ErrAlreadyConnected):
		Error(w, r, http.StatusConflict, "conflict", "instance already connected")
	case errors.Is(err, instance.ErrOwnerNotFound):
		Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "unknown owner")
	case errors.Is(err, instance.ErrInvalidWebhook):
		Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid webhook config")
	case errors.Is(err, instance.ErrNoAdmin):
		Error(w, r, http.StatusInternalServerError, "internal_error", "no admin user exists")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
