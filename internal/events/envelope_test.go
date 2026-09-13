package events

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewEnvelope(t *testing.T) {
	instanceID := uuid.New()
	before := time.Now().UTC()

	env, err := New("connection", instanceID, map[string]any{"status": "connected"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	after := time.Now().UTC()

	if env.EventID == uuid.Nil {
		t.Error("EventID is nil")
	}
	if env.EventVersion != 1 {
		t.Errorf("EventVersion = %d, want 1", env.EventVersion)
	}
	if env.Type != "connection" {
		t.Errorf("Type = %q, want connection", env.Type)
	}
	if env.InstanceID != instanceID {
		t.Errorf("InstanceID = %s, want %s", env.InstanceID, instanceID)
	}
	if env.OccurredAt.Before(before) || env.OccurredAt.After(after) {
		t.Errorf("OccurredAt = %v, want between %v and %v", env.OccurredAt, before, after)
	}
	if loc := env.OccurredAt.Location(); loc != time.UTC {
		t.Errorf("OccurredAt location = %v, want UTC", loc)
	}
}

func TestNewEnvelopeEventIDIsUnique(t *testing.T) {
	seen := make(map[uuid.UUID]bool, 50)
	for i := 0; i < 50; i++ {
		env, err := New("message", uuid.New(), nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[env.EventID] {
			t.Fatalf("EventID %s generated twice", env.EventID)
		}
		seen[env.EventID] = true
	}
}

func TestNewEnvelopeRejectsUnserializablePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{name: "channel", payload: make(chan int)},
		{name: "function", payload: func() {}},
		{name: "infinite float", payload: math.Inf(1)},
		{name: "nan", payload: math.NaN()},
		{name: "complex", payload: complex(1, 2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := New("message", uuid.New(), tt.payload)
			if err == nil {
				t.Fatal("New succeeded with an unserializable payload")
			}
			if env.EventID != uuid.Nil {
				t.Errorf("failed New returned an envelope %+v, want zero value", env)
			}
		})
	}
}

func TestEnvelopeJSONShape(t *testing.T) {
	env, err := New("message", uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal envelope: %v", err)
	}
	wantKeys := []string{"event_id", "event_version", "type", "instance_id", "occurred_at", "payload"}
	if len(raw) != len(wantKeys) {
		t.Errorf("envelope has %d keys, want %d: %s", len(raw), len(wantKeys), data)
	}
	for _, key := range wantKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("envelope is missing key %q: %s", key, data)
		}
	}

	var eventID string
	if err := json.Unmarshal(raw["event_id"], &eventID); err != nil {
		t.Fatalf("decode event_id: %v", err)
	}
	if eventID != env.EventID.String() {
		t.Errorf("event_id = %q, want %q", eventID, env.EventID)
	}

	var eventVersion int
	if err := json.Unmarshal(raw["event_version"], &eventVersion); err != nil {
		t.Fatalf("decode event_version: %v", err)
	}
	if eventVersion != 1 {
		t.Errorf("event_version = %d, want 1", eventVersion)
	}

	var occurred string
	if err := json.Unmarshal(raw["occurred_at"], &occurred); err != nil {
		t.Fatalf("decode occurred_at: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Errorf("occurred_at %q is not RFC3339Nano: %v", occurred, err)
	} else if !parsed.Equal(env.OccurredAt) {
		t.Errorf("occurred_at %q decoded to %v, want %v", occurred, parsed, env.OccurredAt)
	}
	if !strings.HasSuffix(occurred, "Z") {
		t.Errorf("occurred_at = %q, want a UTC timestamp", occurred)
	}
}

func TestNewEnvelopePreservesNestedPayload(t *testing.T) {
	type media struct {
		Mime string `json:"mime"`
		Size int    `json:"size"`
	}
	type payload struct {
		Sender map[string]string `json:"sender"`
		Media  media             `json:"media"`
		Tags   []string          `json:"tags"`
	}

	want := payload{
		Sender: map[string]string{"jid": "5511999999999@s.whatsapp.net"},
		Media:  media{Mime: "image/jpeg", Size: 1234},
		Tags:   []string{"a", "b"},
	}
	env, err := New("message", uuid.New(), want)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got payload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload round trip = %+v, want %+v", got, want)
	}
}

func TestSubjects(t *testing.T) {
	id := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	prefix := "wzap.instances.aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "connection", got: Subjects.Connection(id), want: prefix + ".connection"},
		{name: "message", got: Subjects.Message(id), want: prefix + ".message"},
		{name: "receipt", got: Subjects.Receipt(id), want: prefix + ".receipt"},
		{name: "message status", got: Subjects.MessageStatus(id), want: prefix + ".message.status"},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("Subjects.%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
