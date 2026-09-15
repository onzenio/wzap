package whatsmeow

import (
	"context"
	"strings"
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

// TestPairingTerminalChannelErrorsAreExplicit verifies that terminal QR
// channel failures surface an explicit cause instead of the generic
// "qr channel closed": err-client-outdated, err-scanned-without-multidevice
// and err-unexpected-state move the session to error carrying the reason.
func TestPairingTerminalChannelErrorsAreExplicit(t *testing.T) {
	cases := []struct {
		name string
		item whatsmeow.QRChannelItem
		want string
	}{
		{"client outdated", whatsmeow.QRChannelClientOutdated, "outdated"},
		{"scanned without multidevice", whatsmeow.QRChannelScannedWithoutMultidevice, "multidevice"},
		{"unexpected state", whatsmeow.QRChannelErrUnexpectedEvent, "unexpected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			sess, err := newSession(uuid.New(), &store.Device{}, nil, sink, testMediaLimit)
			if err != nil {
				t.Fatalf("newSession: %v", err)
			}

			pairing := make(chan whatsmeow.QRChannelItem, 4)
			go sess.monitorQR(pairing)
			pairing <- tc.item

			waitForPairing(t, "error status", func() bool {
				return sess.Status() == session.StatusError
			})
			event := sink.last(t)
			if event.status != session.StatusError {
				t.Fatalf("connection event status = %q, want error", event.status)
			}
			if !strings.Contains(event.reason, tc.want) {
				t.Errorf("connection event reason = %q, want it to mention %q", event.reason, tc.want)
			}
		})
	}
}

// TestPairingSurvivesCallerContextCancellation verifies that returning the
// HTTP request which started pairing does not tear down the WhatsApp socket.
// The connection must live on the pairing context and only stop when the
// session cancels the QR flow.
func TestPairingSurvivesCallerContextCancellation(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	pairing := make(chan whatsmeow.QRChannelItem, 1)
	connectedCtx := make(chan context.Context, 1)
	sess.getQRChannelFn = func(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
		go func() {
			<-ctx.Done()
			close(pairing)
		}()
		return pairing, nil
	}
	sess.connectFn = func(ctx context.Context) error {
		connectedCtx <- ctx
		pairing <- qrCodeItem("QR-LIVE")
		return nil
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	code, _, err := sess.Connect(requestCtx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if code != "QR-LIVE" {
		t.Fatalf("QR code = %q, want QR-LIVE", code)
	}

	socketCtx := <-connectedCtx
	cancelRequest()
	select {
	case <-socketCtx.Done():
		t.Fatal("pairing socket context was cancelled with the HTTP request")
	default:
	}

	sess.cancelQR()
	select {
	case <-socketCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("pairing socket context was not cancelled with the QR session")
	}
}
