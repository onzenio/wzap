package message

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/session"
)

// TestReceiptsApplySetsCutEvent pins the shared trimmed event: a small raw
// payload rides the receipt envelope byte-verbatim for NATS and webhook.
func TestReceiptsApplySetsCutEvent(t *testing.T) {
	instanceID := uuid.New()
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	raw := json.RawMessage(`{"type":"delivered"}`)
	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: instanceID,
		MessageIDs: []string{"wamid.1"},
		ChatJID:    "5547988359190@s.whatsapp.net",
		Status:     "delivered",
		Timestamp:  time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Raw:        raw,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	if got := string(written[0].envelope.Event); got != string(raw) {
		t.Errorf("envelope event = %s, want the raw bytes verbatim %s", got, raw)
	}
}

// TestReceiptsApplyCutsOversizedRaw pins the bound on the receipt path: a raw
// event over the media limit is cut to the omission marker.
func TestReceiptsApplyCutsOversizedRaw(t *testing.T) {
	instanceID := uuid.New()
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 16)

	big := strings.Repeat("b", 64)
	raw := json.RawMessage(`{"blob":"` + big + `"}`)
	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: instanceID,
		MessageIDs: []string{"wamid.1"},
		Status:     "delivered",
		Raw:        raw,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	event := string(written[0].envelope.Event)
	if strings.Contains(event, big) {
		t.Error("envelope event still carries the oversized blob")
	}
	if !strings.Contains(event, `"omitted":true`) {
		t.Errorf("envelope event lacks the omission marker: %s", event[:128])
	}
}

// TestOutboxMessageStatusCarriesNoEvent pins that message.status envelopes
// carry no raw upstream payload.
func TestOutboxMessageStatusCarriesNoEvent(t *testing.T) {
	fixture := newOutboxFixture(textMessage(uuid.Nil, 0))

	runOutbox(t, fixture)

	written := fixture.writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	if len(written[0].envelope.Event) != 0 {
		t.Errorf("message.status envelope event = %s, want it absent", written[0].envelope.Event)
	}
}

// TestReceiptsApplyWithoutRawOmitsEvent pins the additive contract on the
// receipt path: no raw means no event key.
func TestReceiptsApplyWithoutRawOmitsEvent(t *testing.T) {
	instanceID := uuid.New()
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: instanceID,
		MessageIDs: []string{"wamid.1"},
		Status:     "delivered",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	if len(written[0].envelope.Event) != 0 {
		t.Errorf("envelope event = %s, want it absent without raw", written[0].envelope.Event)
	}
}
