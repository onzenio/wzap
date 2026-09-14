package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/session"
)

// decodedEditPayload is the decoded inbound message edit event body.
type decodedEditPayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// decodedDeletePayload is the decoded inbound message delete event body.
type decodedDeletePayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Timestamp time.Time `json:"timestamp"`
}

func TestRuntimeOnMessageEditPublishesEditEvent(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024, nil)
	at := time.Date(2026, 9, 14, 12, 5, 0, 0, time.UTC)
	raw := json.RawMessage(`{"edited":"yes"}`)

	runtime.OnMessageEdit(context.Background(), session.MessageEdit{
		InstanceID: id,
		MessageID:  "wamid.orig",
		ChatJID:    "5511999999999@s.whatsapp.net",
		SenderJID:  "5511888888888@s.whatsapp.net",
		Text:       "texto corrigido",
		Timestamp:  at,
		Raw:        raw,
	})

	if len(writer.subjects) != 1 {
		t.Fatalf("event writes = %v, want one edit event", writer.subjects)
	}
	if want := events.Subjects.MessageEdit(id); writer.subjects[0] != want {
		t.Errorf("event subject = %q, want %q", writer.subjects[0], want)
	}
	env := writer.events[0]
	if env.EventVersion != 1 {
		t.Errorf("event_version = %d, want 1", env.EventVersion)
	}
	if env.Type != "message.edit" {
		t.Errorf("event type = %q, want %q", env.Type, "message.edit")
	}
	if env.InstanceID != id {
		t.Errorf("event instance = %s, want %s", env.InstanceID, id)
	}
	if env.EventID == uuid.Nil {
		t.Error("event_id is nil, want a fresh stable id for dedup")
	}
	var payload decodedEditPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode edit payload %q: %v", env.Payload, err)
	}
	if payload.MessageID != "wamid.orig" {
		t.Errorf("payload.message_id = %q, want the original", payload.MessageID)
	}
	if payload.Text != "texto corrigido" {
		t.Errorf("payload.text = %q, want the replacement content", payload.Text)
	}
	if !payload.Timestamp.Equal(at) {
		t.Errorf("payload.timestamp = %v, want the edit moment %v", payload.Timestamp, at)
	}
	if payload.FromJID != "5511888888888@s.whatsapp.net" || payload.ChatJID != "5511999999999@s.whatsapp.net" {
		t.Errorf("payload route = %+v, want the original chat/sender", payload)
	}
	if got := string(env.Event); got != string(raw) {
		t.Errorf("envelope event = %s, want the raw bytes verbatim %s", got, raw)
	}
}

func TestRuntimeOnMessageEditZeroTimestampFallsBackToNow(t *testing.T) {
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "", 1024, nil)
	before := time.Now().UTC()

	runtime.OnMessageEdit(context.Background(), session.MessageEdit{
		InstanceID: uuid.New(), MessageID: "wamid.orig", Text: "novo",
	})
	after := time.Now().UTC()

	var payload decodedEditPayload
	if err := json.Unmarshal(writer.events[0].Payload, &payload); err != nil {
		t.Fatalf("decode edit payload: %v", err)
	}
	if payload.Timestamp.Before(before) || payload.Timestamp.After(after) {
		t.Errorf("payload.timestamp = %v, want between %v and %v", payload.Timestamp, before, after)
	}
}

func TestRuntimeOnMessageDeletePublishesDeleteEvent(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "", 1024, nil)
	at := time.Date(2026, 9, 14, 12, 6, 0, 0, time.UTC)

	runtime.OnMessageDelete(context.Background(), session.MessageDelete{
		InstanceID: id,
		MessageID:  "wamid.gone",
		ChatJID:    "120363000000000000@g.us",
		SenderJID:  "5511888888888@s.whatsapp.net",
		IsGroup:    true,
		Timestamp:  at,
	})

	if len(writer.subjects) != 1 {
		t.Fatalf("event writes = %v, want one delete event", writer.subjects)
	}
	if want := events.Subjects.MessageDelete(id); writer.subjects[0] != want {
		t.Errorf("event subject = %q, want %q", writer.subjects[0], want)
	}
	env := writer.events[0]
	if env.EventVersion != 1 {
		t.Errorf("event_version = %d, want 1", env.EventVersion)
	}
	if env.Type != "message.delete" {
		t.Errorf("event type = %q, want %q", env.Type, "message.delete")
	}
	var payload decodedDeletePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode delete payload %q: %v", env.Payload, err)
	}
	if payload.MessageID != "wamid.gone" {
		t.Errorf("payload.message_id = %q, want the original", payload.MessageID)
	}
	if !payload.Timestamp.Equal(at) {
		t.Errorf("payload.timestamp = %v, want the revocation moment %v", payload.Timestamp, at)
	}
	if !payload.IsGroup || payload.ChatJID != "120363000000000000@g.us" {
		t.Errorf("payload route = %+v, want the group chat", payload)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &raw); err != nil {
		t.Fatalf("decode raw delete payload: %v", err)
	}
	if _, ok := raw["text"]; ok {
		t.Errorf("delete payload carries text, want only the original id: %s", env.Payload)
	}
}

func TestRuntimeOnMessageEditWriteFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	writer := &fakeWriter{writeErr: errors.New("outbox down")}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "", 1024,
		slog.New(slog.NewTextHandler(&logs, nil)))

	runtime.OnMessageEdit(context.Background(), session.MessageEdit{
		InstanceID: uuid.New(), MessageID: "wamid.orig",
	})
	runtime.OnMessageDelete(context.Background(), session.MessageDelete{
		InstanceID: uuid.New(), MessageID: "wamid.gone",
	})

	if !strings.Contains(logs.String(), "enqueue message edit event") {
		t.Errorf("logs = %q, want the edit enqueue failure", logs.String())
	}
	if !strings.Contains(logs.String(), "enqueue message delete event") {
		t.Errorf("logs = %q, want the delete enqueue failure", logs.String())
	}
}
