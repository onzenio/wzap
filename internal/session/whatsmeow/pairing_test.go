package whatsmeow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"wzap/internal/session"
)

// waitForPairing polls cond until it holds or the deadline passes.
func waitForPairing(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func qrCodeItem(code string) whatsmeow.QRChannelItem {
	return whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: code, Timeout: time.Minute}
}

func currentQR(sess *instanceSession) (string, error) {
	code, _, err := sess.QR(context.Background())
	return code, err
}

// TestPairingQRReissuedAfterExpiry verifies task 4.5: when a QR code expires,
// the next pairing round delivers a fresh code that replaces the expired one.
func TestPairingQRReissuedAfterExpiry(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	first := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(first)
	first <- qrCodeItem("QR-ONE")
	waitForPairing(t, "first QR code", func() bool {
		code, err := currentQR(sess)
		return err == nil && code == "QR-ONE"
	})

	first <- whatsmeow.QRChannelTimeout
	waitForPairing(t, "expired QR code", func() bool {
		_, err := currentQR(sess)
		return err != nil
	})
	if sess.Status() != session.StatusDisconnected {
		t.Fatalf("session status = %q, want disconnected after expiry", sess.Status())
	}

	second := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(second)
	second <- qrCodeItem("QR-TWO")
	waitForPairing(t, "reissued QR code", func() bool {
		code, err := currentQR(sess)
		return err == nil && code == "QR-TWO"
	})
	close(second)
}

// TestPairingSuccessRegistersPublicJID verifies task 4.5: reading the QR code
// moves the session to connected, records the public identifier and publishes
// the connection event.
func TestPairingSuccessRegistersPublicJID(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	device := &store.Device{ID: &jid}
	sink := &recordingSink{}
	sess, err := newSession(uuid.New(), device, nil, sink, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	pairing := make(chan whatsmeow.QRChannelItem, 4)
	go sess.monitorQR(pairing)
	pairing <- qrCodeItem("QR-PAIR")
	pairing <- whatsmeow.QRChannelSuccess

	waitForPairing(t, "pairing success event", func() bool {
		return sink.count() > 0
	})
	if sess.Status() != session.StatusConnected {
		t.Fatalf("session status = %q, want connected", sess.Status())
	}
	if sess.JID() != jid.String() {
		t.Fatalf("session JID = %q, want paired %q", sess.JID(), jid.String())
	}
	event := sink.last(t)
	if event.status != session.StatusConnected {
		t.Errorf("connection event status = %q, want connected", event.status)
	}
	if event.jid != jid.String() {
		t.Errorf("connection event JID = %q, want %q", event.jid, jid.String())
	}
}
