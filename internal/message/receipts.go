package message

import (
	"context"
	"fmt"
	"time"

	"wzap/internal/events"
	"wzap/internal/session"
	"wzap/internal/storage"
	"wzap/internal/webhook"
)

// receiptEventType is the event type of the inbound receipt events.
const receiptEventType = "receipt"

// ReceiptStore records the delivery/read milestones of outbound messages.
type ReceiptStore interface {
	// UpdateReceipt maps delivered to delivered_at and read/played to read_at
	// by WhatsApp message id. It reports false when no message matches or the
	// status is unknown.
	UpdateReceipt(ctx context.Context, whatsAppMessageID, status string, at time.Time) (bool, error)
}

// The concrete repository satisfies the receipt contract; the interface
// assertion catches signature drift at build time.
var _ ReceiptStore = (storage.MessageRepository)(nil)

// Receipts projects the delivery/read acknowledgements of a session into the
// stored messages and the event outbox. maxMediaBytes bounds the trimmed raw
// upstream event carried by the receipt envelope (see Apply).
type Receipts struct {
	repo          ReceiptStore
	writer        events.Writer
	maxMediaBytes int64
}

// NewReceipts builds the receipt projector over its dependencies.
func NewReceipts(repo ReceiptStore, writer events.Writer, maxMediaBytes int64) *Receipts {
	return &Receipts{repo: repo, writer: writer, maxMediaBytes: maxMediaBytes}
}

// Apply updates every message the receipt refers to and enqueues one receipt
// event with the ids that matched. A receipt for messages that are not ours is
// ignored: the update reports no match and no event is written. The timestamp
// falls back to now when WhatsApp sends none.
func (r *Receipts) Apply(ctx context.Context, receipt session.Receipt) error {
	if len(receipt.MessageIDs) == 0 {
		return nil
	}

	at := receipt.Timestamp
	if at.IsZero() {
		at = time.Now().UTC()
	}

	matched := make([]string, 0, len(receipt.MessageIDs))
	for _, whatsAppID := range receipt.MessageIDs {
		updated, err := r.repo.UpdateReceipt(ctx, whatsAppID, receipt.Status, at)
		if err != nil {
			return fmt.Errorf("apply receipt %s: %w", whatsAppID, err)
		}
		if updated {
			matched = append(matched, whatsAppID)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	env, err := events.New(receiptEventType, receipt.InstanceID, receiptPayload{
		MessageIDs: matched,
		Status:     receipt.Status,
		ChatJID:    receipt.ChatJID,
		Timestamp:  at,
	})
	if err != nil {
		return fmt.Errorf("build receipt event: %w", err)
	}
	// The trimmed raw rides the envelope for NATS and webhook alike, so both
	// consumers share the same bounded event.
	if trimmed, _ := webhook.CutRawForLimit(receipt.Raw, r.maxMediaBytes); len(trimmed) > 0 {
		env.Event = trimmed
	}
	if err := r.writer.Write(ctx, events.Subjects.Receipt(receipt.InstanceID), env); err != nil {
		return fmt.Errorf("enqueue receipt event: %w", err)
	}
	return nil
}

// receiptPayload is the JSON body of a receipt event. MessageIDs carries the
// WhatsApp message ids that were updated; consumers correlate them with the
// whatsapp_id of the message.status events.
type receiptPayload struct {
	MessageIDs []string  `json:"message_ids"`
	Status     string    `json:"status"`
	ChatJID    string    `json:"chat_jid,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}
