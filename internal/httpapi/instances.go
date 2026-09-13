package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"wzap/internal/instance"
	"wzap/internal/model"
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
	Create(ctx context.Context, input instance.CreateInput) (*model.Instance, error)
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

// instanceResponse is the JSON representation of an instance.
type instanceResponse struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	ExternalRef     string     `json:"external_ref"`
	Status          string     `json:"status"`
	WhatsAppJID     string     `json:"whatsapp_jid"`
	LastError       string     `json:"last_error"`
	LastConnectedAt *time.Time `json:"last_connected_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// instanceListResponse is the JSON representation of an instance page.
type instanceListResponse struct {
	Items      []instanceResponse `json:"items"`
	NextCursor string             `json:"next_cursor"`
}

// createInstanceRequest is the POST /instances payload.
type createInstanceRequest struct {
	Name        string `json:"name"`
	ExternalRef string `json:"external_ref"`
}

// updateInstanceRequest uses pointers so an omitted field keeps its stored
// value while an explicit empty external_ref clears it.
type updateInstanceRequest struct {
	Name        *string `json:"name"`
	ExternalRef *string `json:"external_ref"`
}

// handleCreateInstance registers an instance and answers 201 with it.
func handleCreateInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request createInstanceRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		created, err := instances.Create(r.Context(), instance.CreateInput{
			Name:        request.Name,
			ExternalRef: request.ExternalRef,
		})
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusCreated, newInstanceResponse(created))
	}
}

// handleListInstances answers one page of instances with its next cursor.
func handleListInstances(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, next, err := instances.List(r.Context(),
			parseInstancesLimit(r.URL.Query().Get("limit")), r.URL.Query().Get("cursor"))
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}

		response := instanceListResponse{Items: make([]instanceResponse, 0, len(items)), NextCursor: next}
		for i := range items {
			response.Items = append(response.Items, newInstanceResponse(&items[i]))
		}
		JSON(w, http.StatusOK, response)
	}
}

// handleGetInstance answers one instance by id.
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
		JSON(w, http.StatusOK, newInstanceResponse(found))
	}
}

// handleUpdateInstance applies a partial update and answers 200 with the stored
// instance.
func handleUpdateInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		var request updateInstanceRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		updated, err := instances.Update(r.Context(), id, instance.UpdateInput{
			Name:        request.Name,
			ExternalRef: request.ExternalRef,
		})
		if err != nil {
			writeInstanceError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, newInstanceResponse(updated))
	}
}

// handleDeleteInstance removes an instance and answers 204.
func handleDeleteInstance(instances InstanceService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		if err := instances.Delete(r.Context(), id); err != nil {
			writeInstanceError(w, r, err)
			return
		}
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

// newInstanceResponse maps a stored instance to its JSON representation.
func newInstanceResponse(inst *model.Instance) instanceResponse {
	return instanceResponse{
		ID:              inst.ID.String(),
		Name:            inst.Name,
		ExternalRef:     inst.ExternalRef,
		Status:          inst.Status,
		WhatsAppJID:     inst.WhatsAppJID,
		LastError:       inst.LastError,
		LastConnectedAt: inst.LastConnectedAt,
		CreatedAt:       inst.CreatedAt,
		UpdatedAt:       inst.UpdatedAt,
	}
}

// writeInstanceError maps a service error to its HTTP status and error
// envelope. Unknown failures answer 500 without leaking their cause.
func writeInstanceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, instance.ErrNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
	case errors.Is(err, instance.ErrExternalRefTaken):
		Error(w, r, http.StatusConflict, "conflict", "external ref already taken")
	case errors.Is(err, instance.ErrInvalidCursor):
		Error(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
	case errors.Is(err, instance.ErrAlreadyConnected):
		Error(w, r, http.StatusConflict, "conflict", "instance already connected")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
