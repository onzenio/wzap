package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

// Message types accepted by Enqueue and persisted in message_queue.type.
const (
	TypeText     = "text"
	TypeLocation = "location"
	TypeContact  = "contact"
	TypeMedia    = "media"
)

// Message statuses persisted in message_queue.status.
const (
	// StatusQueued is the initial state of an accepted message.
	StatusQueued = "queued"
	// StatusSending means a worker claimed the message for delivery.
	StatusSending = "sending"
	// StatusSent means WhatsApp accepted the message.
	StatusSent = "sent"
	// StatusFailed means the message was not delivered and will not be retried.
	StatusFailed = "failed"
)

// Errors reported by the service and mapped to HTTP status codes by the handler
// layer.
var (
	// ErrInstanceNotFound reports that the enqueue target does not exist.
	ErrInstanceNotFound = errors.New("instance not found")
	// ErrInstanceNotConnected reports that the instance cannot send because its
	// session is not connected.
	ErrInstanceNotConnected = errors.New("instance not connected")
	// ErrMessageNotFound reports that the queried message does not exist or
	// belongs to another instance.
	ErrMessageNotFound = errors.New("message not found")
	// ErrInvalidCursor reports that a list cursor is not a valid identifier.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrInvalidInput reports that the message content is malformed or
	// unsupported.
	ErrInvalidInput = errors.New("invalid message input")
)

// InstanceReader reads the instance a message is enqueued for.
type InstanceReader interface {
	Get(ctx context.Context, id uuid.UUID) (*model.Instance, error)
}

// Resolver resolves a recipient phone number to its canonical WhatsApp JID.
type Resolver interface {
	Resolve(ctx context.Context, instanceID uuid.UUID, phone string) (jid string, err error)
}

// MessageStore persists and queries the outbound message queue.
type MessageStore interface {
	Create(ctx context.Context, message model.OutboundMessage) (*model.OutboundMessage, error)
	Get(ctx context.Context, id uuid.UUID) (*model.OutboundMessage, error)
	ListByInstance(ctx context.Context, instanceID uuid.UUID, limit int, cursor string) ([]model.OutboundMessage, string, error)
}

// EnqueueInput is the content accepted by Enqueue. Only the fields of the
// chosen Type are used; the rest are ignored.
type EnqueueInput struct {
	Type        string
	To          string
	Text        string
	Caption     string
	Filename    string
	PTT         bool
	Latitude    float64
	Longitude   float64
	DisplayName string
	VCard       string
	MediaID     *uuid.UUID
}

// Service accepts outbound messages: it validates the target instance, resolves
// the recipient JID and persists the message as queued for the outbox workers.
type Service struct {
	instances InstanceReader
	resolver  Resolver
	messages  MessageStore
}

// The concrete resolver satisfies the service contract; the assertion catches
// signature drift at build time.
var _ Resolver = (*JIDResolver)(nil)

// NewService builds the service over its dependencies.
func NewService(instances InstanceReader, resolver Resolver, messages MessageStore) *Service {
	return &Service{instances: instances, resolver: resolver, messages: messages}
}

// Enqueue validates and stores one message, returning its identifier. A
// disconnected instance is a conflict; an unresolved recipient or an invalid
// payload is rejected without storing anything. The stored recipient is the
// canonical JID returned by the resolver.
func (s *Service) Enqueue(ctx context.Context, instanceID uuid.UUID, input EnqueueInput) (uuid.UUID, error) {
	instance, err := s.instances.Get(ctx, instanceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return uuid.Nil, fmt.Errorf("enqueue message: %w", ErrInstanceNotFound)
		}
		return uuid.Nil, fmt.Errorf("enqueue message: get instance: %w", err)
	}
	if instance.Status != string(session.StatusConnected) {
		return uuid.Nil, fmt.Errorf("enqueue message: instance %s is %s: %w", instanceID, instance.Status, ErrInstanceNotConnected)
	}

	payload, err := buildPayload(input)
	if err != nil {
		return uuid.Nil, err
	}

	jid, err := s.resolver.Resolve(ctx, instanceID, input.To)
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue message: %w", err)
	}

	created, err := s.messages.Create(ctx, model.OutboundMessage{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		Type:         input.Type,
		RecipientJID: jid,
		Payload:      payload,
		MediaID:      input.MediaID,
		Status:       StatusQueued,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue message: create: %w", err)
	}
	return created.ID, nil
}

// Get returns a message of instanceID or ErrMessageNotFound. A message that
// belongs to another instance is hidden so the query cannot cross instances.
func (s *Service) Get(ctx context.Context, instanceID, messageID uuid.UUID) (*model.OutboundMessage, error) {
	message, err := s.messages.Get(ctx, messageID)
	if err != nil {
		return nil, mapMessageError("get message", err)
	}
	if message.InstanceID != instanceID {
		return nil, fmt.Errorf("get message: %w", ErrMessageNotFound)
	}
	return message, nil
}

// List returns a page of instanceID messages and the cursor of the next page,
// empty on the last page.
func (s *Service) List(ctx context.Context, instanceID uuid.UUID, limit int, cursor string) ([]model.OutboundMessage, string, error) {
	messages, next, err := s.messages.ListByInstance(ctx, instanceID, limit, cursor)
	if err != nil {
		return nil, "", mapMessageError("list messages", err)
	}
	return messages, next, nil
}

// buildPayload validates the content of input and serializes the type-specific
// JSON body stored in message_queue.payload. An unsupported type or missing
// required content is ErrInvalidInput.
func buildPayload(input EnqueueInput) ([]byte, error) {
	switch input.Type {
	case TypeText:
		if strings.TrimSpace(input.Text) == "" {
			return nil, fmt.Errorf("%w: text is required", ErrInvalidInput)
		}
		return json.Marshal(textPayload{Text: input.Text})
	case TypeLocation:
		if math.IsNaN(input.Latitude) || input.Latitude < -90 || input.Latitude > 90 {
			return nil, fmt.Errorf("%w: latitude out of range", ErrInvalidInput)
		}
		if math.IsNaN(input.Longitude) || input.Longitude < -180 || input.Longitude > 180 {
			return nil, fmt.Errorf("%w: longitude out of range", ErrInvalidInput)
		}
		return json.Marshal(locationPayload{Latitude: input.Latitude, Longitude: input.Longitude})
	case TypeContact:
		if strings.TrimSpace(input.DisplayName) == "" {
			return nil, fmt.Errorf("%w: display_name is required", ErrInvalidInput)
		}
		if strings.TrimSpace(input.VCard) == "" {
			return nil, fmt.Errorf("%w: vcard is required", ErrInvalidInput)
		}
		return json.Marshal(contactPayload{DisplayName: input.DisplayName, VCard: input.VCard})
	case TypeMedia:
		if input.MediaID == nil {
			return nil, fmt.Errorf("%w: media_id is required", ErrInvalidInput)
		}
		return json.Marshal(mediaPayload{Caption: input.Caption, Filename: input.Filename, PTT: input.PTT})
	default:
		return nil, fmt.Errorf("%w: unsupported type %q", ErrInvalidInput, input.Type)
	}
}

// textPayload is the stored body of a text message.
type textPayload struct {
	Text string `json:"text"`
}

// locationPayload is the stored body of a location message.
type locationPayload struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// contactPayload is the stored body of a contact message.
type contactPayload struct {
	DisplayName string `json:"display_name"`
	VCard       string `json:"vcard"`
}

// mediaPayload is the stored body of a media message. The mimetype is resolved
// from the media row by the sender.
type mediaPayload struct {
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
	PTT      bool   `json:"ptt,omitempty"`
}

// mapMessageError translates a storage error into the service sentinel the HTTP
// layer maps to a status code, preserving the operation context for the logs.
func mapMessageError(op string, err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%s: %w", op, ErrMessageNotFound)
	case errors.Is(err, storage.ErrInvalidCursor):
		return fmt.Errorf("%s: %w", op, ErrInvalidCursor)
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
