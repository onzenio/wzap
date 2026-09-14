package sessiontest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
)

var errBoom = errors.New("boom")

type connectionCall struct {
	instanceID uuid.UUID
	status     session.Status
	jid        string
	reason     string
}

type recordingSink struct {
	messages    []session.InboundMessage
	edits       []session.MessageEdit
	deletes     []session.MessageDelete
	receipts    []session.Receipt
	connections []connectionCall
}

func (r *recordingSink) OnMessage(_ context.Context, msg session.InboundMessage) {
	r.messages = append(r.messages, msg)
}

func (r *recordingSink) OnMessageEdit(_ context.Context, edit session.MessageEdit) {
	r.edits = append(r.edits, edit)
}

func (r *recordingSink) OnMessageDelete(_ context.Context, del session.MessageDelete) {
	r.deletes = append(r.deletes, del)
}

func (r *recordingSink) OnReceipt(_ context.Context, receipt session.Receipt) {
	r.receipts = append(r.receipts, receipt)
}

func (r *recordingSink) OnConnection(_ context.Context, instanceID uuid.UUID, status session.Status, jid string, reason string) {
	r.connections = append(r.connections, connectionCall{instanceID, status, jid, reason})
}

func TestFakeManagerStoresAndRecordsSessions(t *testing.T) {
	sink := &recordingSink{}
	fake := New(sink)
	instance := &model.Instance{ID: uuid.New(), Name: "acme"}

	sess, err := fake.Create(instance)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess == nil {
		t.Fatal("Create returned a nil session")
	}

	got, ok := fake.Get(instance.ID)
	if !ok || got != sess {
		t.Fatalf("Get after Create = (%v, %v), want the created session", got, ok)
	}

	calls := fake.CreateCalls()
	if len(calls) != 1 || calls[0] != instance {
		t.Fatalf("CreateCalls = %v, want [%v]", calls, instance)
	}

	if _, ok := fake.Get(uuid.New()); ok {
		t.Fatal("Get for an unknown instance reported found")
	}

	if err := fake.Remove(context.Background(), instance.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := fake.Get(instance.ID); ok {
		t.Fatal("removed session is still returned by Get")
	}
	removed := fake.RemoveCalls()
	if len(removed) != 1 || removed[0] != instance.ID {
		t.Fatalf("RemoveCalls = %v, want [%v]", removed, instance.ID)
	}
}

func TestFakeManagerRestoreAll(t *testing.T) {
	fake := New(nil)

	if err := fake.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if got := fake.RestoreCalls(); got != 1 {
		t.Fatalf("RestoreCalls = %d, want 1", got)
	}

	fake.RestoreAllErr = errBoom
	if err := fake.RestoreAll(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RestoreAll forced error = %v, want %v", err, errBoom)
	}
	if got := fake.RestoreCalls(); got != 2 {
		t.Fatalf("RestoreCalls after forced error = %d, want 2", got)
	}
}

func TestFakeManagerForcedErrors(t *testing.T) {
	fake := New(nil)
	fake.CreateErr = errBoom
	if _, err := fake.Create(&model.Instance{ID: uuid.New()}); !errors.Is(err, errBoom) {
		t.Fatalf("Create forced error = %v, want %v", err, errBoom)
	}

	fake.RemoveErr = errBoom
	if err := fake.Remove(context.Background(), uuid.New()); !errors.Is(err, errBoom) {
		t.Fatalf("Remove forced error = %v, want %v", err, errBoom)
	}
}

func TestFakeSessionConnectAndQR(t *testing.T) {
	fake := New(nil)
	sess, err := fake.Create(&model.Instance{ID: uuid.New()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fakeSession := sess.(*FakeSession)

	if got := fakeSession.Status(); got != session.StatusDisconnected {
		t.Fatalf("initial status = %q, want %q", got, session.StatusDisconnected)
	}

	ctx := context.Background()
	qr, expiresAt, err := fakeSession.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if qr == "" {
		t.Fatal("Connect returned an empty QR code")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("QR expiry %v is not in the future", expiresAt)
	}
	if got := fakeSession.Status(); got != session.StatusPairing {
		t.Fatalf("status after Connect = %q, want %q", got, session.StatusPairing)
	}
	if got := fakeSession.ConnectCalls(); got != 1 {
		t.Fatalf("ConnectCalls = %d, want 1", got)
	}

	code, codeExpiry, err := fakeSession.QR(ctx)
	if err != nil {
		t.Fatalf("QR: %v", err)
	}
	if code != qr || !codeExpiry.Equal(expiresAt) {
		t.Fatalf("QR = (%q, %v), want (%q, %v)", code, codeExpiry, qr, expiresAt)
	}

	fakeSession.ConnectErr = errBoom
	if _, _, err := fakeSession.Connect(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("Connect forced error = %v, want %v", err, errBoom)
	}
}

func TestFakeSessionSendAndRecipients(t *testing.T) {
	fake := New(nil)
	sess, _ := fake.Create(&model.Instance{ID: uuid.New()})
	fakeSession := sess.(*FakeSession)
	ctx := context.Background()

	out := session.OutboundMessage{Type: "text", RecipientJID: "5511@s.whatsapp.net", Payload: []byte(`{"text":"oi"}`)}
	waID, err := fakeSession.Send(ctx, out)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if waID == "" {
		t.Fatal("Send returned an empty WhatsApp id")
	}
	sent := fakeSession.SendCalls()
	if len(sent) != 1 || sent[0].RecipientJID != out.RecipientJID {
		t.Fatalf("SendCalls = %v, want the outbound message", sent)
	}

	fakeSession.SendErr = errBoom
	if _, err := fakeSession.Send(ctx, out); !errors.Is(err, errBoom) {
		t.Fatalf("Send forced error = %v, want %v", err, errBoom)
	}

	fakeSession.OnWhatsApp = map[string]string{"5511999999999": "5511999999999@s.whatsapp.net"}
	jid, ok, err := fakeSession.IsOnWhatsApp(ctx, "5511999999999")
	if err != nil || !ok || jid != "5511999999999@s.whatsapp.net" {
		t.Fatalf("IsOnWhatsApp known = (%q, %v, %v)", jid, ok, err)
	}
	if _, ok, err := fakeSession.IsOnWhatsApp(ctx, "5511888888888"); err != nil || ok {
		t.Fatalf("IsOnWhatsApp unknown = (ok=%v, err=%v), want not found", ok, err)
	}
	fakeSession.IsOnWhatsAppErr = errBoom
	if _, _, err := fakeSession.IsOnWhatsApp(ctx, "5511999999999"); !errors.Is(err, errBoom) {
		t.Fatalf("IsOnWhatsApp forced error = %v, want %v", err, errBoom)
	}

	if err := fakeSession.SendPresence(ctx, "5511@s.whatsapp.net", "composing"); err != nil {
		t.Fatalf("SendPresence: %v", err)
	}
	presences := fakeSession.PresenceCalls()
	if len(presences) != 1 || presences[0].State != "composing" {
		t.Fatalf("PresenceCalls = %v, want one composing call", presences)
	}

	if err := fakeSession.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := fakeSession.Status(); got != session.StatusDisconnected {
		t.Fatalf("status after Disconnect = %q, want %q", got, session.StatusDisconnected)
	}
	if got := fakeSession.DisconnectCalls(); got != 1 {
		t.Fatalf("DisconnectCalls = %d, want 1", got)
	}
}

func TestFakeSessionEmitsEventsToSink(t *testing.T) {
	sink := &recordingSink{}
	instance := &model.Instance{ID: uuid.New(), Name: "acme"}
	fake := New(sink)
	sess, _ := fake.Create(instance)
	fakeSession := sess.(*FakeSession)

	message := session.InboundMessage{InstanceID: instance.ID, MessageID: "m1", ChatJID: "5511@s.whatsapp.net", Text: "oi"}
	fakeSession.EmitMessage(message)
	receipt := session.Receipt{InstanceID: instance.ID, MessageIDs: []string{"m1"}, Status: "read"}
	fakeSession.EmitReceipt(receipt)
	fakeSession.EmitConnection(session.StatusConnected, "5511@s.whatsapp.net", "")

	if len(sink.messages) != 1 || sink.messages[0].MessageID != "m1" {
		t.Fatalf("sink messages = %v, want the emitted message", sink.messages)
	}
	if len(sink.receipts) != 1 || sink.receipts[0].Status != "read" {
		t.Fatalf("sink receipts = %v, want the emitted receipt", sink.receipts)
	}
	want := connectionCall{instance.ID, session.StatusConnected, "5511@s.whatsapp.net", ""}
	if len(sink.connections) != 1 || sink.connections[0] != want {
		t.Fatalf("sink connections = %v, want [%v]", sink.connections, want)
	}
}

func TestFakeSessionWithoutSinkIsSafe(t *testing.T) {
	fake := New(nil)
	sess, _ := fake.Create(&model.Instance{ID: uuid.New()})
	fakeSession := sess.(*FakeSession)

	fakeSession.EmitMessage(session.InboundMessage{})
	fakeSession.EmitReceipt(session.Receipt{})
	fakeSession.EmitConnection(session.StatusError, "", "boom")
}
