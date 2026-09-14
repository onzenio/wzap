package whatsmeow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// TestInboundMessageCapturesRaw pins the webhook raw capture: the translated
// message carries the best-effort JSON of the upstream event.
func TestInboundMessageCapturesRaw(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("5511999999999", types.DefaultUserServer),
				Sender: types.NewJID("5511888888888", types.DefaultUserServer),
			},
			ID:        "3EB0RAW",
			Type:      "text",
			Timestamp: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{Conversation: proto.String("raw please")},
	}

	got := inboundMessage(uuid.New(), evt, nil, testMediaLimit)
	if len(got.Raw) == 0 {
		t.Fatal("inbound Raw is empty, want the captured upstream event")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("inbound Raw is not JSON: %v", err)
	}
}

// TestReceiptCapturesRaw pins the receipt raw capture: the translated receipt
// carries the best-effort JSON of the upstream event.
func TestReceiptCapturesRaw(t *testing.T) {
	evt := &events.Receipt{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID("5511999999999", types.DefaultUserServer),
			Sender: types.NewJID("5511888888888", types.DefaultUserServer),
		},
		MessageIDs: []types.MessageID{"A1"},
		Timestamp:  time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Type:       types.ReceiptTypeDelivered,
	}

	got := receiptEvent(uuid.New(), evt)
	if len(got.Raw) == 0 {
		t.Fatal("receipt Raw is empty, want the captured upstream event")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("receipt Raw is not JSON: %v", err)
	}
}
