package mirror

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
)

const (
	// qrPNGSize is the pixel width/height of the QR render attached to
	// pairing notices. 256px scans reliably while staying small.
	qrPNGSize = 256
)

// QRProvider fetches the live pairing QR string of an instance at
// handle time. Production wires it to the session manager
// (Session.QR, backed by the in-memory pairing state); the worker only
// calls it for pairing notices whose QRImage arrived empty. A nil
// provider disables the lookup and every pairing notice goes text-only.
type QRProvider interface {
	QRCode(ctx context.Context, instanceID uuid.UUID) (code string, expiresAt time.Time, err error)
}

// EncodeQR renders a WhatsApp pairing QR string as PNG bytes. An empty
// string is an error; the caller falls back to the text-only notice.
func EncodeQR(code string) ([]byte, error) {
	if code == "" {
		return nil, errors.New("encode qr: empty code")
	}
	qr, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	return qr.PNG(qrPNGSize)
}
