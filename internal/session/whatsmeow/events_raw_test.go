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

// TestInboundMessageCapturesStickerMedia pins that stickers flow through
// the media path: the session exposes the sticker bytes for download instead
// of dropping the message as unmappable further down the pipeline.
func TestInboundMessageCapturesStickerMedia(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("5511999999999", types.DefaultUserServer),
				Sender: types.NewJID("5511888888888", types.DefaultUserServer),
			},
			ID:        "3EB0STICKER",
			Type:      "media",
			Timestamp: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			Mimetype:   proto.String("image/webp"),
			FileLength: proto.Uint64(1234),
		}},
	}

	got := inboundMessage(uuid.New(), evt, nil, testMediaLimit)
	if !got.MediaAvailable {
		t.Fatal("sticker MediaAvailable = false, want true (stickers ride the media path)")
	}
	if got.MediaMime != "image/webp" {
		t.Errorf("sticker MediaMime = %q, want image/webp", got.MediaMime)
	}
	if got.MediaFilename != "sticker.webp" {
		t.Errorf("sticker MediaFilename = %q, want the sticker fallback name", got.MediaFilename)
	}
	if got.MediaLength != 1234 {
		t.Errorf("sticker MediaLength = %d, want 1234", got.MediaLength)
	}
	if got.MediaDownload != nil {
		t.Error("sticker MediaDownload is set with a nil client, want nil")
	}
	if got.Text != "" {
		t.Errorf("sticker Text = %q, want empty (no caption to mirror)", got.Text)
	}
}
