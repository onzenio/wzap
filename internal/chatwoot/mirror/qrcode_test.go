package mirror

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeQRProvider replays one QR string or a lookup failure.
type fakeQRProvider struct {
	code  string
	err   error
	calls int
}

func (f *fakeQRProvider) QRCode(context.Context, uuid.UUID) (string, time.Time, error) {
	f.calls++
	return f.code, time.Now().Add(time.Minute), f.err
}

var pngMagic = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func TestEncodeQRProducesPNG(t *testing.T) {
	png, err := EncodeQR("wzap-pairing-code")
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	if len(png) == 0 {
		t.Fatal("EncodeQR returned empty bytes")
	}
	if !bytes.HasPrefix(png, pngMagic) {
		t.Errorf("EncodeQR bytes do not start with the PNG magic: %x", png[:8])
	}
}

func TestEncodeQREmptyCodeFails(t *testing.T) {
	if _, err := EncodeQR(""); err == nil {
		t.Error("EncodeQR(\"\") = nil, want an error")
	}
}

func TestHandleConnectionPairingFetchesQRFromProvider(t *testing.T) {
	fx := newFixture(nil)
	qr := &fakeQRProvider{code: "wzap-pairing-code"}
	fx.worker.qr = qr

	notice := ConnectionNotice{Status: "pairing", PairingCode: "ABCD-1234"}
	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	if qr.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", qr.calls)
	}
	atts := fx.cli.attachments()
	if len(atts) != 1 {
		t.Fatalf("attachment calls = %d, want 1 (rendered QR image)", len(atts))
	}
	got := atts[0].req
	if got.ContentType != "image/png" || got.FileName != "qrcode.png" {
		t.Errorf("attachment = %s/%s, want image/png qrcode.png", got.ContentType, got.FileName)
	}
	if !bytes.HasPrefix(got.File, pngMagic) {
		t.Error("attached QR bytes do not start with the PNG magic")
	}
	if !contains(got.Content, "ABCD-1234") {
		t.Errorf("QR notice = %q, want the pairing code in text", got.Content)
	}
}

func TestHandleConnectionPairingProviderErrorFallsBackToText(t *testing.T) {
	fx := newFixture(nil)
	qr := &fakeQRProvider{err: errors.New("no qr code available")}
	fx.worker.qr = qr

	notice := ConnectionNotice{Status: "pairing"}
	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("provider failure must fall back to text, got error: %v", err)
	}
	if n := len(fx.cli.attachments()); n != 0 {
		t.Errorf("attachment calls = %d, want 0 (text fallback)", n)
	}
	calls := fx.cli.creates()
	if len(calls) != 1 {
		t.Fatalf("CreateMessage calls = %d, want 1 (text notice)", len(calls))
	}
	if !contains(calls[0].req.Content, "Pareamento") {
		t.Errorf("notice = %q, want the pt-BR pairing text", calls[0].req.Content)
	}
}

func TestHandleConnectionNonPairingSkipsProvider(t *testing.T) {
	fx := newFixture(nil)
	qr := &fakeQRProvider{code: "wzap-pairing-code"}
	fx.worker.qr = qr

	notice := ConnectionNotice{Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}
	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	if qr.calls != 0 {
		t.Errorf("provider calls = %d, want 0 (only pairing notices consult it)", qr.calls)
	}
	if n := len(fx.cli.creates()); n != 1 {
		t.Errorf("CreateMessage calls = %d, want 1 (text notice)", n)
	}
}

func TestHandleConnectionPairingKeepsAttachedImageWithoutProvider(t *testing.T) {
	fx := newFixture(nil)
	qr := &fakeQRProvider{code: "should-not-be-used"}
	fx.worker.qr = qr

	notice := ConnectionNotice{Status: "pairing", QRImage: append([]byte(nil), pngMagic...)}
	if err := fx.worker.HandleConnection(context.Background(), uuid.New(), uuid.New(), notice); err != nil {
		t.Fatalf("HandleConnection: %v", err)
	}
	if qr.calls != 0 {
		t.Errorf("provider calls = %d, want 0 (attached image wins)", qr.calls)
	}
	if n := len(fx.cli.attachments()); n != 1 {
		t.Errorf("attachment calls = %d, want 1 (attached QR image)", n)
	}
}
