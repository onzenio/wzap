// Package events defines the wzap event envelope, the broker subjects and the
// outbox pipeline that carries events to NATS JetStream.
package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// currentEventVersion is the schema version stamped on every new envelope.
const currentEventVersion = 1

// Envelope is the versioned JSON document published for every event.
type Envelope struct {
	EventID      uuid.UUID       `json:"event_id"`
	EventVersion int             `json:"event_version"`
	Type         string          `json:"type"`
	InstanceID   uuid.UUID       `json:"instance_id"`
	OccurredAt   time.Time       `json:"occurred_at"`
	Payload      json.RawMessage `json:"payload"`
	// Event carries the raw upstream event (e.g. the serialized whatsmeow
	// payload) for webhook delivery. It is additive to the NATS contract:
	// omitempty keeps it off the wire when nil, so existing broker
	// serialization is unchanged. Only message/receipt envelopes set it;
	// connection-type envelopes carry no event.
	Event json.RawMessage `json:"event,omitempty"`
}

// New builds an envelope of the given event type and instance, serializing the
// payload to JSON. The event id is a fresh UUID and occurred_at is the current
// UTC time with nanosecond precision.
func New(eventType string, instanceID uuid.UUID, payload any) (Envelope, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal payload: %w", err)
	}
	return Envelope{
		EventID:      uuid.New(),
		EventVersion: currentEventVersion,
		Type:         eventType,
		InstanceID:   instanceID,
		OccurredAt:   time.Now().UTC(),
		Payload:      data,
	}, nil
}

// bytes marshals the envelope for the outbox or the broker.
func (e Envelope) bytes() ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope %s: %w", e.EventID, err)
	}
	return data, nil
}
