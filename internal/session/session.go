// Package session defines the WhatsApp session contracts consumed by the wzap
// services. The whatsmeow implementation lives in the whatsmeow subpackage so
// the library types stay confined to it.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
)

// Status is the lifecycle state of an instance session.
type Status string

const (
	// StatusDisconnected means the session has no active connection.
	StatusDisconnected Status = "disconnected"
	// StatusPairing means the session is waiting for a QR code to be scanned.
	StatusPairing Status = "pairing"
	// StatusConnected means the session is authenticated and online.
	StatusConnected Status = "connected"
	// StatusError means the session stopped on a failure that needs attention.
	StatusError Status = "error"
)

// Session errors classified by the outbox workers and the services.
var (
	// ErrTransient marks a failure that may succeed on a retry.
	ErrTransient = errors.New("session transient error")
	// ErrNotConnected marks an operation attempted on a disconnected session.
	ErrNotConnected = errors.New("session not connected")
	// ErrInvalidRecipient marks a malformed recipient JID.
	ErrInvalidRecipient = errors.New("session invalid recipient")
	// ErrNoDevice marks an instance whose persisted device is gone, for
	// example after an external logout or a device removal. The pairing cannot
	// be resumed and the instance must be paired again.
	ErrNoDevice = errors.New("session device not found")
)

// OutboundMessage is the normalized message handed to a session for delivery.
// Payload carries the type-specific JSON body: text {"text"}, location
// {"latitude","longitude","name","address"}, contact {"display_name","vcard"}
// and media {"caption","filename","mime_type","ptt"}, with MediaPath pointing
// at the file on disk for media types.
type OutboundMessage struct {
	Type         string
	RecipientJID string
	Payload      []byte
	MediaPath    string
}

// InboundMessage is a received message translated away from the library types.
type InboundMessage struct {
	InstanceID uuid.UUID
	MessageID  string
	ChatJID    string
	SenderJID  string
	IsGroup    bool
	Type       string
	Text       string
	Timestamp  time.Time

	MediaAvailable bool
	MediaMime      string
	MediaFilename  string
	// MediaLength is the size in bytes announced by the source, or zero when
	// it is unknown. It lets the consumer reject oversized media before
	// downloading it.
	MediaLength int64
	// MediaDownload fetches the media bytes on demand. It is nil when the
	// message carries no downloadable media.
	MediaDownload func(ctx context.Context) ([]byte, error)
}

// Receipt is a delivery/read acknowledgement for previously sent messages.
type Receipt struct {
	InstanceID uuid.UUID
	MessageIDs []string
	ChatJID    string
	SenderJID  string
	Status     string
	Timestamp  time.Time
}

// EventSink consumes session events. Implementations must be safe for
// concurrent use and should not block the session for long.
type EventSink interface {
	OnMessage(ctx context.Context, msg InboundMessage)
	OnReceipt(ctx context.Context, receipt Receipt)
	OnConnection(ctx context.Context, instanceID uuid.UUID, status Status, jid string, reason string)
}

// Session is a single instance connection.
type Session interface {
	// Connect starts pairing and returns the first QR code and its expiry. It
	// returns an empty QR when the instance already has stored credentials and
	// only needs to be brought online.
	Connect(ctx context.Context) (qr string, expiresAt time.Time, err error)
	// QR returns the current pairing QR code and its expiry.
	QR(ctx context.Context) (string, time.Time, error)
	// Send delivers an outbound message and returns the WhatsApp message id.
	Send(ctx context.Context, msg OutboundMessage) (whatsappID string, err error)
	// IsOnWhatsApp checks whether a phone number is registered on WhatsApp,
	// returning its canonical JID when it is.
	IsOnWhatsApp(ctx context.Context, phone string) (jid string, ok bool, err error)
	// SendPresence reports chat presence ("composing"/"paused") or user
	// presence ("available"/"unavailable") for a chat.
	SendPresence(ctx context.Context, chatJID, state string) error
	// Disconnect asks WhatsApp to log the companion device out, then closes
	// the connection. A session that was never online or whose device is
	// already gone disconnects locally without failing.
	Disconnect(ctx context.Context) error
	// Status returns the current lifecycle state.
	Status() Status
	// JID returns the public WhatsApp JID, empty while pairing.
	JID() string
}

// Manager owns the session of every instance.
type Manager interface {
	// RestoreAll reconnects the sessions persisted in the database, with
	// limited concurrency.
	RestoreAll(ctx context.Context) error
	// Get returns the session of instanceID when one exists.
	Get(instanceID uuid.UUID) (Session, bool)
	// Create returns the session of instance, building a new device when the
	// instance was never paired. Repeating it for a known instance returns the
	// existing session.
	Create(instance *model.Instance) (Session, error)
	// Remove tears the session down and deletes its stored credentials.
	Remove(ctx context.Context, instanceID uuid.UUID) error
}
