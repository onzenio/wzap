package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
)

// TestRuntimeOnMessageSetsCutEvent pins the shared trimmed event: a small raw
// payload rides the envelope byte-verbatim for NATS and webhook alike.
func TestRuntimeOnMessageSetsCutEvent(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1<<20, nil)

	raw := json.RawMessage(`{"message":{"conversation":"hello"}}`)
	runtime.OnMessage(context.Background(), session.InboundMessage{
		InstanceID: id,
		MessageID:  "wamid.raw",
		ChatJID:    "5511999999999@s.whatsapp.net",
		SenderJID:  "5511888888888@s.whatsapp.net",
		Type:       "text",
		Text:       "hello",
		Timestamp:  time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Raw:        raw,
	})

	if len(writer.events) != 1 {
		t.Fatalf("events written = %d, want 1", len(writer.events))
	}
	if got := string(writer.events[0].Event); got != string(raw) {
		t.Errorf("envelope event = %s, want the raw bytes verbatim %s", got, raw)
	}
}

// TestRuntimeOnMessageCutsOversizedRaw pins the bound: a raw event with a
// string over the media limit is cut to the omission marker.
func TestRuntimeOnMessageCutsOversizedRaw(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 16, nil)

	big := strings.Repeat("a", 64)
	raw := json.RawMessage(`{"blob":"` + big + `"}`)
	runtime.OnMessage(context.Background(), session.InboundMessage{
		InstanceID: id,
		MessageID:  "wamid.big",
		ChatJID:    "5511999999999@s.whatsapp.net",
		SenderJID:  "5511888888888@s.whatsapp.net",
		Type:       "text",
		Raw:        raw,
	})

	if len(writer.events) != 1 {
		t.Fatalf("events written = %d, want 1", len(writer.events))
	}
	event := string(writer.events[0].Event)
	if strings.Contains(event, big) {
		t.Errorf("envelope event still carries the oversized blob: %s", event)
	}
	if !strings.Contains(event, `"omitted":true`) {
		t.Errorf("envelope event lacks the omission marker: %s", event)
	}
}

// TestRuntimeOnMessageWithoutRawOmitsEvent pins the additive contract: no raw
// means no event key for the existing NATS consumers.
func TestRuntimeOnMessageWithoutRawOmitsEvent(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1<<20, nil)

	runtime.OnMessage(context.Background(), session.InboundMessage{
		InstanceID: id,
		MessageID:  "wamid.plain",
		ChatJID:    "5511999999999@s.whatsapp.net",
		SenderJID:  "5511888888888@s.whatsapp.net",
		Type:       "text",
		Text:       "hello",
		Timestamp:  time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
	})

	if len(writer.events) != 1 {
		t.Fatalf("events written = %d, want 1", len(writer.events))
	}
	if len(writer.events[0].Event) != 0 {
		t.Errorf("envelope event = %s, want it absent without raw", writer.events[0].Event)
	}
}

// TestRuntimeOnConnectionCarriesNoEvent pins that connection envelopes carry
// no raw upstream payload.
func TestRuntimeOnConnectionCarriesNoEvent(t *testing.T) {
	id := uuid.New()
	repo := newRuntimeRepo(model.Instance{
		ID: id, Name: "loja", Status: string(session.StatusDisconnected),
	})
	writer := &fakeWriter{}
	runtime := NewRuntime(repo, writer, nil, nil, "", 0, nil)

	runtime.OnConnection(context.Background(), id, session.StatusConnected, "5511999999999@s.whatsapp.net", "")

	if len(writer.events) != 1 {
		t.Fatalf("events written = %d, want 1", len(writer.events))
	}
	if len(writer.events[0].Event) != 0 {
		t.Errorf("connection envelope event = %s, want it absent", writer.events[0].Event)
	}
}
