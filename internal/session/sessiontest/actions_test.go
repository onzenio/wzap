package sessiontest

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/session"
)

// TestFakeSessionDeleteMessageRecordsCalls pins the fake contract the reverse
// delete worker relies on: every call is recorded with its arguments.
func TestFakeSessionDeleteMessageRecordsCalls(t *testing.T) {
	sess := NewSession(uuid.New(), nil)

	if err := sess.DeleteMessage(context.Background(), "5511999999999@s.whatsapp.net", "ORIG-1"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if err := sess.DeleteMessage(context.Background(), "120363000000000000@g.us", "ORIG-2"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	calls := sess.DeleteCalls()
	if len(calls) != 2 {
		t.Fatalf("DeleteCalls = %+v, want 2 calls", calls)
	}
	if calls[0].ChatJID != "5511999999999@s.whatsapp.net" || calls[0].MessageID != "ORIG-1" {
		t.Errorf("DeleteCalls[0] = %+v, want chat/message ORIG-1", calls[0])
	}
	if calls[1].ChatJID != "120363000000000000@g.us" || calls[1].MessageID != "ORIG-2" {
		t.Errorf("DeleteCalls[1] = %+v, want group/message ORIG-2", calls[1])
	}
}

// TestFakeSessionDeleteMessageForcedError pins failure injection for the
// delete path.
func TestFakeSessionDeleteMessageForcedError(t *testing.T) {
	sess := NewSession(uuid.New(), nil)
	sess.DeleteMessageErr = errBoom

	if err := sess.DeleteMessage(context.Background(), "chat", "id"); err != errBoom {
		t.Fatalf("DeleteMessage error = %v, want the forced boom", err)
	}
}

// TestFakeSessionMarkReadRecordsCalls pins the fake contract the read-sync
// worker relies on.
func TestFakeSessionMarkReadRecordsCalls(t *testing.T) {
	sess := NewSession(uuid.New(), nil)

	if err := sess.MarkRead(context.Background(), "120363000000000000@g.us", "5511888888888@s.whatsapp.net", "ORIG-9"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	calls := sess.MarkReadCalls()
	if len(calls) != 1 {
		t.Fatalf("MarkReadCalls = %+v, want 1 call", calls)
	}
	call := calls[0]
	if call.ChatJID != "120363000000000000@g.us" || call.SenderJID != "5511888888888@s.whatsapp.net" || call.MessageID != "ORIG-9" {
		t.Errorf("MarkReadCalls[0] = %+v, want chat/sender/message", call)
	}
}

// TestFakeSessionMarkReadForcedError pins failure injection for the read path.
func TestFakeSessionMarkReadForcedError(t *testing.T) {
	sess := NewSession(uuid.New(), nil)
	sess.MarkReadErr = errBoom

	if err := sess.MarkRead(context.Background(), "chat", "sender", "id"); err != errBoom {
		t.Fatalf("MarkRead error = %v, want the forced boom", err)
	}
}

// TestFakeSessionPairPhoneReturnsCode pins the pairing-code contract: the
// configured code is returned and the number recorded.
func TestFakeSessionPairPhoneReturnsCode(t *testing.T) {
	sess := NewSession(uuid.New(), nil)
	sess.PairPhoneCode = "ABCD1234"

	code, err := sess.PairPhone(context.Background(), "5511999999999")
	if err != nil {
		t.Fatalf("PairPhone: %v", err)
	}
	if code != "ABCD1234" {
		t.Errorf("PairPhone code = %q, want the configured ABCD1234", code)
	}
	calls := sess.PairPhoneCalls()
	if len(calls) != 1 || calls[0].Number != "5511999999999" {
		t.Errorf("PairPhoneCalls = %+v, want the dialed number", calls)
	}
}

// TestFakeSessionPairPhoneDefaultCodeIsStable pins that an unconfigured fake
// still returns a usable code instead of an empty string.
func TestFakeSessionPairPhoneDefaultCodeIsStable(t *testing.T) {
	sess := NewSession(uuid.New(), nil)

	code, err := sess.PairPhone(context.Background(), "5511999999999")
	if err != nil {
		t.Fatalf("PairPhone: %v", err)
	}
	if code == "" {
		t.Error("PairPhone code is empty, want the stable default")
	}
}

// TestFakeSessionPairPhoneForcedError pins failure injection for pairing.
func TestFakeSessionPairPhoneForcedError(t *testing.T) {
	sess := NewSession(uuid.New(), nil)
	sess.PairPhoneErr = errBoom

	if _, err := sess.PairPhone(context.Background(), "5511999999999"); err != errBoom {
		t.Fatalf("PairPhone error = %v, want the forced boom", err)
	}
}

// TestFakeSessionEmitsEditDelete pins that synthesized edits/deletes reach
// the sink, which is how the mirror tests drive the runtime.
func TestFakeSessionEmitsEditDelete(t *testing.T) {
	sink := &recordingSink{}
	sess := NewSession(uuid.New(), sink)

	sess.EmitMessageEdit(session.MessageEdit{MessageID: "ORIG-E", Text: "novo"})
	sess.EmitMessageDelete(session.MessageDelete{MessageID: "ORIG-D"})

	if len(sink.edits) != 1 || sink.edits[0].MessageID != "ORIG-E" {
		t.Errorf("sink edits = %+v, want ORIG-E", sink.edits)
	}
	if len(sink.deletes) != 1 || sink.deletes[0].MessageID != "ORIG-D" {
		t.Errorf("sink deletes = %+v, want ORIG-D", sink.deletes)
	}
}
