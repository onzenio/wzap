package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/session"
)

// updateReceiptCall records one repository UpdateReceipt call.
type updateReceiptCall struct {
	whatsAppID string
	status     string
	at         time.Time
}

// fakeReceiptRepo is an in-memory ReceiptStore. known holds the WhatsApp ids
// that belong to a stored message; like the SQL repository it reports false
// for a status it cannot project.
type fakeReceiptRepo struct {
	known     map[string]bool
	calls     []updateReceiptCall
	updateErr error
}

func (f *fakeReceiptRepo) UpdateReceipt(_ context.Context, whatsAppID, status string, at time.Time) (bool, error) {
	f.calls = append(f.calls, updateReceiptCall{whatsAppID: whatsAppID, status: status, at: at})
	if f.updateErr != nil {
		return false, f.updateErr
	}
	switch status {
	case "delivered", "read", "played":
		return f.known[whatsAppID], nil
	default:
		return false, nil
	}
}

func (f *fakeReceiptRepo) updateCalls() []updateReceiptCall {
	return append([]updateReceiptCall(nil), f.calls...)
}

func TestReceiptsApplyUpdatesMessageAndEmitsEvent(t *testing.T) {
	instanceID := uuid.New()
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: instanceID,
		MessageIDs: []string{"wamid.1"},
		ChatJID:    "5547988359190@s.whatsapp.net",
		Status:     "delivered",
		Timestamp:  at,
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls := repo.updateCalls()
	if len(calls) != 1 {
		t.Fatalf("UpdateReceipt calls = %d, want 1", len(calls))
	}
	if calls[0].whatsAppID != "wamid.1" || calls[0].status != "delivered" || !calls[0].at.Equal(at) {
		t.Errorf("UpdateReceipt call = %+v, want wamid.1 delivered at %s", calls[0], at)
	}

	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	event := written[0]
	if want := events.Subjects.Receipt(instanceID); event.subject != want {
		t.Errorf("event subject = %q, want %q", event.subject, want)
	}
	if event.envelope.Type != receiptEventType {
		t.Errorf("event type = %q, want %q", event.envelope.Type, receiptEventType)
	}
	if event.envelope.InstanceID != instanceID {
		t.Errorf("event instance = %s, want %s", event.envelope.InstanceID, instanceID)
	}
	payload := envelopePayload(t, event.envelope)
	if payload["status"] != "delivered" {
		t.Errorf("payload status = %v, want delivered", payload["status"])
	}
	if payload["chat_jid"] != "5547988359190@s.whatsapp.net" {
		t.Errorf("payload chat_jid = %v, want the chat", payload["chat_jid"])
	}
	if got := payloadMessageIDs(t, payload); len(got) != 1 || got[0] != "wamid.1" {
		t.Errorf("payload message_ids = %v, want [wamid.1]", got)
	}
	stamp, err := time.Parse(time.RFC3339Nano, payload["timestamp"].(string))
	if err != nil {
		t.Fatalf("payload timestamp = %v, want a RFC3339 timestamp", payload["timestamp"])
	}
	if !stamp.Equal(at) {
		t.Errorf("payload timestamp = %s, want %s", stamp, at)
	}
}

func TestReceiptsApplyProjectsEveryStatus(t *testing.T) {
	for _, status := range []string{"delivered", "read", "played"} {
		t.Run(status, func(t *testing.T) {
			repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
			writer := &fakeWriter{}
			receipts := NewReceipts(repo, writer, 1<<20)

			err := receipts.Apply(context.Background(), session.Receipt{
				InstanceID: uuid.New(),
				MessageIDs: []string{"wamid.1"},
				Status:     status,
				Timestamp:  time.Now().UTC(),
			})

			if err != nil {
				t.Fatalf("Apply(%s): %v", status, err)
			}
			if len(repo.updateCalls()) != 1 || repo.updateCalls()[0].status != status {
				t.Errorf("UpdateReceipt calls = %+v, want the %s status", repo.updateCalls(), status)
			}
			if got := len(writer.written()); got != 1 {
				t.Fatalf("events written = %d, want 1", got)
			}
			if payload := envelopePayload(t, writer.written()[0].envelope); payload["status"] != status {
				t.Errorf("payload status = %v, want %s", payload["status"], status)
			}
		})
	}
}

func TestReceiptsApplyEmitsOnlyOwnMessages(t *testing.T) {
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.ours": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.ours", "wamid.foreign"},
		Status:     "read",
		Timestamp:  time.Now().UTC(),
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := len(repo.updateCalls()); got != 2 {
		t.Fatalf("UpdateReceipt calls = %d, want 2", got)
	}
	written := writer.written()
	if len(written) != 1 {
		t.Fatalf("events written = %d, want 1", len(written))
	}
	ids := payloadMessageIDs(t, envelopePayload(t, written[0].envelope))
	if len(ids) != 1 || ids[0] != "wamid.ours" {
		t.Errorf("payload message_ids = %v, want only wamid.ours", ids)
	}
}

func TestReceiptsApplyIgnoresForeignMessages(t *testing.T) {
	repo := &fakeReceiptRepo{known: map[string]bool{}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.foreign"},
		Status:     "read",
		Timestamp:  time.Now().UTC(),
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := len(writer.written()); got != 0 {
		t.Errorf("events written = %d, want none for a foreign receipt", got)
	}
}

func TestReceiptsApplyIgnoresNonProjectableStatus(t *testing.T) {
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.1"},
		Status:     "retry",
		Timestamp:  time.Now().UTC(),
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := len(writer.written()); got != 0 {
		t.Errorf("events written = %d, want none for a non-projectable status", got)
	}
}

func TestReceiptsApplyWithoutMessageIDsDoesNothing(t *testing.T) {
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{InstanceID: uuid.New()})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(repo.updateCalls()) != 0 || len(writer.written()) != 0 {
		t.Errorf("Apply with no ids did %d repo calls and %d event writes, want none",
			len(repo.updateCalls()), len(writer.written()))
	}
}

func TestReceiptsApplyRepositoryFailureReturnsError(t *testing.T) {
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}, updateErr: errors.New("database down")}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.1"},
		Status:     "delivered",
		Timestamp:  time.Now().UTC(),
	})

	if err == nil {
		t.Fatal("Apply error = nil, want the repository failure")
	}
	if got := len(writer.written()); got != 0 {
		t.Errorf("events written = %d, want none when the update failed", got)
	}
}

func TestReceiptsApplyWriterFailureReturnsError(t *testing.T) {
	errWrite := errors.New("broker down")
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{writeErr: errWrite}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.1"},
		Status:     "delivered",
		Timestamp:  time.Now().UTC(),
	})

	if !errors.Is(err, errWrite) {
		t.Errorf("Apply error = %v, want the writer failure", err)
	}
}

func TestReceiptsApplyDefaultsZeroTimestamp(t *testing.T) {
	before := time.Now().UTC()
	repo := &fakeReceiptRepo{known: map[string]bool{"wamid.1": true}}
	writer := &fakeWriter{}
	receipts := NewReceipts(repo, writer, 1<<20)

	err := receipts.Apply(context.Background(), session.Receipt{
		InstanceID: uuid.New(),
		MessageIDs: []string{"wamid.1"},
		Status:     "read",
	})

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := repo.updateCalls()
	if len(calls) != 1 || calls[0].at.IsZero() || calls[0].at.Before(before) {
		t.Errorf("UpdateReceipt at = %v, want the current time", calls[0].at)
	}
}

// payloadMessageIDs decodes the message_ids array of a receipt payload.
func payloadMessageIDs(t *testing.T, payload map[string]any) []string {
	t.Helper()

	raw, ok := payload["message_ids"].([]any)
	if !ok {
		t.Fatalf("payload message_ids = %v, want an array", payload["message_ids"])
	}
	ids := make([]string, len(raw))
	for i, value := range raw {
		id, ok := value.(string)
		if !ok {
			t.Fatalf("payload message_ids[%d] = %v, want a string", i, value)
		}
		ids[i] = id
	}
	return ids
}
