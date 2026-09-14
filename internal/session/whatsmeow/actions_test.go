package whatsmeow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"wzap/internal/session"
)

// revokeCapture records one revoke send through the delegation seam.
type revokeCapture struct {
	chat types.JID
	msg  *waE2E.Message
}

// markReadCapture records one read receipt through the delegation seam.
type markReadCapture struct {
	ids       []types.MessageID
	timestamp time.Time
	chat      types.JID
	sender    types.JID
}

func actionSession(t *testing.T) *instanceSession {
	t.Helper()
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	return sess
}

// TestDeleteMessageInvalidChatJID pins the argument guard: a malformed chat
// never reaches the library. ParseJID in the pinned lib only splits
// user/server, so the empty JID is the invalid input it can actually catch.
func TestDeleteMessageInvalidChatJID(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.sendRevokeFn = func(context.Context, types.JID, *waE2E.Message) (whatsmeow.SendResponse, error) {
		called = true
		return whatsmeow.SendResponse{}, nil
	}

	err := sess.DeleteMessage(context.Background(), "", "ORIG-1")
	if !errors.Is(err, session.ErrInvalidRecipient) {
		t.Fatalf("DeleteMessage error = %v, want %v", err, session.ErrInvalidRecipient)
	}
	if called {
		t.Error("the revoke reached the library with an invalid chat JID")
	}
}

// TestDeleteMessageEmptyIDIsRejected pins that a revoke without the original
// id is rejected before touching the library.
func TestDeleteMessageEmptyIDIsRejected(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.sendRevokeFn = func(context.Context, types.JID, *waE2E.Message) (whatsmeow.SendResponse, error) {
		called = true
		return whatsmeow.SendResponse{}, nil
	}

	if err := sess.DeleteMessage(context.Background(), "5511999999999@s.whatsapp.net", ""); err == nil {
		t.Fatal("DeleteMessage with an empty id succeeded, want an error")
	}
	if called {
		t.Error("the revoke reached the library with an empty message id")
	}
}

// TestDeleteMessageBuildsRevokeForOriginal pins the delegation: the session
// builds a REVOKE protocol message keyed by the original id (RevokeMessage is
// deprecated in the pinned lib in favor of BuildRevoke + SendMessage) and
// sends it to the chat.
func TestDeleteMessageBuildsRevokeForOriginal(t *testing.T) {
	sess := actionSession(t)
	var captured *revokeCapture
	sess.sendRevokeFn = func(_ context.Context, chat types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
		captured = &revokeCapture{chat: chat, msg: msg}
		return whatsmeow.SendResponse{}, nil
	}

	chat := "5511999999999@s.whatsapp.net"
	if err := sess.DeleteMessage(context.Background(), chat, "ORIG-7"); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	if captured == nil {
		t.Fatal("no revoke was sent through the delegation seam")
	}
	if captured.chat.String() != chat {
		t.Errorf("revoke chat = %q, want %q", captured.chat, chat)
	}
	protoMsg := captured.msg.GetProtocolMessage()
	if protoMsg == nil || protoMsg.GetType() != waE2E.ProtocolMessage_REVOKE {
		t.Fatalf("revoke message = %v, want a REVOKE protocol message", captured.msg)
	}
	if got := protoMsg.GetKey().GetID(); got != "ORIG-7" {
		t.Errorf("revoke key id = %q, want the original ORIG-7", got)
	}
}

// TestDeleteMessageSendFailureIsClassified pins error mapping: a dead socket
// surfaces as not-connected, anything else as transient.
func TestDeleteMessageSendFailureIsClassified(t *testing.T) {
	sess := actionSession(t)
	sess.sendRevokeFn = func(context.Context, types.JID, *waE2E.Message) (whatsmeow.SendResponse, error) {
		return whatsmeow.SendResponse{}, whatsmeow.ErrNotConnected
	}
	if err := sess.DeleteMessage(context.Background(), "5511999999999@s.whatsapp.net", "ORIG-1"); !errors.Is(err, session.ErrNotConnected) {
		t.Errorf("DeleteMessage error = %v, want %v", err, session.ErrNotConnected)
	}

	sess.sendRevokeFn = func(context.Context, types.JID, *waE2E.Message) (whatsmeow.SendResponse, error) {
		return whatsmeow.SendResponse{}, errors.New("socket reset")
	}
	if err := sess.DeleteMessage(context.Background(), "5511999999999@s.whatsapp.net", "ORIG-1"); !errors.Is(err, session.ErrTransient) {
		t.Errorf("DeleteMessage error = %v, want %v", err, session.ErrTransient)
	}
}

// TestMarkReadInvalidChatJID pins the argument guard of the read path.
func TestMarkReadInvalidChatJID(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.markReadFn = func(context.Context, []types.MessageID, time.Time, types.JID, types.JID) error {
		called = true
		return nil
	}

	err := sess.MarkRead(context.Background(), "", "sender", "ORIG-1")
	if !errors.Is(err, session.ErrInvalidRecipient) {
		t.Fatalf("MarkRead error = %v, want %v", err, session.ErrInvalidRecipient)
	}
	if called {
		t.Error("the receipt reached the library with an invalid chat JID")
	}
}

// TestMarkReadInvalidSenderJID pins the author guard of the read path: "@"
// parses to an empty JID in the pinned lib, so it is rejected explicitly.
func TestMarkReadInvalidSenderJID(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.markReadFn = func(context.Context, []types.MessageID, time.Time, types.JID, types.JID) error {
		called = true
		return nil
	}

	err := sess.MarkRead(context.Background(), "120363000000000000@g.us", "@", "ORIG-1")
	if !errors.Is(err, session.ErrInvalidRecipient) {
		t.Fatalf("MarkRead error = %v, want %v", err, session.ErrInvalidRecipient)
	}
	if called {
		t.Error("the receipt reached the library with an invalid sender JID")
	}
}

// TestMarkReadEmptyIDIsRejected pins that a receipt without the message id is
// rejected before touching the library.
func TestMarkReadEmptyIDIsRejected(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.markReadFn = func(context.Context, []types.MessageID, time.Time, types.JID, types.JID) error {
		called = true
		return nil
	}

	if err := sess.MarkRead(context.Background(), "5511999999999@s.whatsapp.net", "", ""); err == nil {
		t.Fatal("MarkRead with an empty id succeeded, want an error")
	}
	if called {
		t.Error("the receipt reached the library with an empty message id")
	}
}

// TestMarkReadSendsSingleIDWithTimestamp pins the delegation shape of the
// pinned lib (ids, read-at time, chat, sender): one message per call, stamped
// at read time.
func TestMarkReadSendsSingleIDWithTimestamp(t *testing.T) {
	sess := actionSession(t)
	var captured *markReadCapture
	sess.markReadFn = func(_ context.Context, ids []types.MessageID, ts time.Time, chat, sender types.JID) error {
		captured = &markReadCapture{ids: ids, timestamp: ts, chat: chat, sender: sender}
		return nil
	}

	before := time.Now()
	chat := "120363000000000000@g.us"
	sender := "5511888888888@s.whatsapp.net"
	if err := sess.MarkRead(context.Background(), chat, sender, "ORIG-3"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	if captured == nil {
		t.Fatal("no receipt was sent through the delegation seam")
	}
	if len(captured.ids) != 1 || string(captured.ids[0]) != "ORIG-3" {
		t.Errorf("receipt ids = %v, want [ORIG-3]", captured.ids)
	}
	if captured.chat.String() != chat {
		t.Errorf("receipt chat = %q, want %q", captured.chat, chat)
	}
	if captured.sender.String() != sender {
		t.Errorf("receipt sender = %q, want %q", captured.sender, sender)
	}
	if captured.timestamp.Before(before) || captured.timestamp.After(time.Now().Add(time.Second)) {
		t.Errorf("receipt timestamp = %v, want the read time after %v", captured.timestamp, before)
	}
}

// TestMarkReadEmptySenderFallsBackToChat pins the DM ergonomics: without an
// explicit author the chat itself is reported as the sender.
func TestMarkReadEmptySenderFallsBackToChat(t *testing.T) {
	sess := actionSession(t)
	var captured *markReadCapture
	sess.markReadFn = func(_ context.Context, ids []types.MessageID, ts time.Time, chat, sender types.JID) error {
		captured = &markReadCapture{ids: ids, timestamp: ts, chat: chat, sender: sender}
		return nil
	}

	chat := "5511999999999@s.whatsapp.net"
	if err := sess.MarkRead(context.Background(), chat, "", "ORIG-4"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if captured == nil {
		t.Fatal("no receipt was sent through the delegation seam")
	}
	if captured.sender.String() != chat {
		t.Errorf("receipt sender = %q, want the chat %q", captured.sender, chat)
	}
}

// TestMarkReadFailureIsClassified pins error mapping of the read path.
func TestMarkReadFailureIsClassified(t *testing.T) {
	sess := actionSession(t)
	sess.markReadFn = func(context.Context, []types.MessageID, time.Time, types.JID, types.JID) error {
		return whatsmeow.ErrNotLoggedIn
	}
	if err := sess.MarkRead(context.Background(), "5511999999999@s.whatsapp.net", "", "ORIG-1"); !errors.Is(err, session.ErrNotConnected) {
		t.Errorf("MarkRead error = %v, want %v", err, session.ErrNotConnected)
	}
}

// TestPairPhoneBlankNumber pins the argument guard: nothing is requested
// without a phone number.
func TestPairPhoneBlankNumber(t *testing.T) {
	sess := actionSession(t)
	called := false
	sess.pairPhoneFn = func(_ context.Context, phone string) (string, error) {
		called = true
		return "SHOULD-NOT-HAPPEN", nil
	}

	if _, err := sess.PairPhone(context.Background(), "   "); err == nil {
		t.Fatal("PairPhone with a blank number succeeded, want an error")
	}
	if called {
		t.Error("the pairing request reached the library with a blank number")
	}
}

// TestPairPhoneReturnsCode pins the delegation: the number goes to the
// library PairPhone and the code comes back untouched.
func TestPairPhoneReturnsCode(t *testing.T) {
	sess := actionSession(t)
	var gotPhone string
	sess.pairPhoneFn = func(_ context.Context, phone string) (string, error) {
		gotPhone = phone
		return "ABCD1234", nil
	}

	code, err := sess.PairPhone(context.Background(), "5511999999999")
	if err != nil {
		t.Fatalf("PairPhone: %v", err)
	}
	if code != "ABCD1234" {
		t.Errorf("PairPhone code = %q, want the library code", code)
	}
	if gotPhone != "5511999999999" {
		t.Errorf("PairPhone number = %q, want the dialed number", gotPhone)
	}
}

// TestPairPhoneFailureIsClassified pins error mapping of the pairing path.
func TestPairPhoneFailureIsClassified(t *testing.T) {
	sess := actionSession(t)
	sess.pairPhoneFn = func(context.Context, string) (string, error) {
		return "", whatsmeow.ErrNotConnected
	}
	if _, err := sess.PairPhone(context.Background(), "5511999999999"); !errors.Is(err, session.ErrNotConnected) {
		t.Errorf("PairPhone error = %v, want %v", err, session.ErrNotConnected)
	}
}
