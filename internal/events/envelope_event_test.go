package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestEnvelopeEventOmittedWhenNil locks the additive NATS contract: a nil
// Event serializes to nothing, so existing consumers see the exact six keys.
func TestEnvelopeEventOmittedWhenNil(t *testing.T) {
	env := Envelope{
		EventID:      uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		EventVersion: 1,
		Type:         "message",
		InstanceID:   uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		OccurredAt:   time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Payload:      json.RawMessage(`{"text":"hi"}`),
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"event"`) {
		t.Errorf("envelope with nil Event contains an event key: %s", data)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal envelope: %v", err)
	}
	if len(raw) != 6 {
		t.Errorf("envelope has %d keys, want 6 (nil Event must stay absent): %s", len(raw), data)
	}
}

// TestEnvelopeEventPresentWhenSet locks the webhook path: when raw bytes are
// available the envelope carries them verbatim under "event".
func TestEnvelopeEventPresentWhenSet(t *testing.T) {
	rawEvent := json.RawMessage(`{"message":{"conversation":"hello"}}`)
	env := Envelope{
		EventID:      uuid.New(),
		EventVersion: 1,
		Type:         "message",
		InstanceID:   uuid.New(),
		OccurredAt:   time.Now().UTC(),
		Payload:      json.RawMessage(`{"text":"hello"}`),
		Event:        rawEvent,
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal envelope: %v", err)
	}
	got, ok := raw["event"]
	if !ok {
		t.Fatalf("envelope is missing key %q: %s", "event", data)
	}
	if string(got) != string(rawEvent) {
		t.Errorf("event = %s, want %s", got, rawEvent)
	}
}
