// Package model defines the persistence-agnostic types shared by the wzap
// storage and services.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Instance is a WhatsApp instance registered with the wzap service.
type Instance struct {
	ID              uuid.UUID
	Name            string
	ExternalRef     string
	Status          string
	WhatsAppJID     string
	LastError       string
	LastConnectedAt *time.Time
	// OwnerUserID is the manager user owning the instance. It stays nil for
	// legacy rows created before the product migration backfilled owners.
	OwnerUserID *uuid.UUID
	// WebhookURL is the per-instance webhook endpoint. Nil means unconfigured.
	WebhookURL *string
	// WebhookEnabled toggles delivery to WebhookURL. WebhookEvents lists the
	// subscribed event types. The instance key hash is never exposed here;
	// it stays inside the API key repository methods.
	WebhookEnabled bool
	WebhookEvents  []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// User is a manager account. PasswordHash is a repo-level credential detail and
// must never be logged or exposed beyond the storage boundary.
type User struct {
	ID            uuid.UUID
	Email         string
	PasswordHash  string
	Role          string
	InstanceQuota int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OutboundMessage is a message queued for delivery through an instance.
type OutboundMessage struct {
	ID                uuid.UUID
	InstanceID        uuid.UUID
	Type              string
	RecipientJID      string
	Payload           []byte
	MediaID           *uuid.UUID
	Status            string
	WhatsAppMessageID string
	LastError         string
	Attempts          int
	DeliveredAt       *time.Time
	ReadAt            *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Media is a stored media object. Its bytes live on the filesystem at
// StoragePath, relative to the configured data dir; the row keeps the metadata
// and the checksum of the content.
type Media struct {
	ID          uuid.UUID
	InstanceID  uuid.UUID
	Direction   string
	MessageID   string
	Mimetype    string
	Filename    string
	SizeBytes   int64
	StoragePath string
	SHA256      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// IdempotencyRecord is the stored outcome of a request accepted under an
// idempotency key. Status is "in_progress" while the original request runs and
// "completed" once its response was captured. ResponseStatus and ResponseBody
// are only meaningful when completed.
type IdempotencyRecord struct {
	InstanceID     uuid.UUID
	Key            string
	Fingerprint    string
	Status         string
	ResponseStatus int
	ResponseBody   []byte
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// OutboxEvent is an event awaiting publication to the broker. PublishedAt is
// nil until the relay publishes it; Attempts counts failed publish attempts.
type OutboxEvent struct {
	ID          uuid.UUID
	Subject     string
	Envelope    []byte
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// ChatwootConfig is the per-instance Chatwoot connector configuration. Token
// is stored in clear text by design (encryption is a later version).
type ChatwootConfig struct {
	InstanceID          uuid.UUID
	Enabled             bool
	URL                 string
	AccountID           string
	Token               string
	NameInbox           string
	SignMsg             bool
	SignDelimiter       string
	ReopenConversation  bool
	ConversationPending bool
	MergeBrazilContacts bool
	ImportContacts      bool
	ImportMessages      bool
	DaysLimit           int
	AutoCreate          bool
	Organization        string
	Logo                string
	IgnoreJIDs          []string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ChatwootMessage correlates a WhatsApp message key with its Chatwoot mirror.
// Rows are never purged; they disappear on instance DELETE via cascade.
type ChatwootMessage struct {
	InstanceID        uuid.UUID
	WAKey             string
	ChatwootMessageID int64
	ConversationID    int64
	InboxID           int64
	ContactSourceID   string
	IsRead            bool
	CreatedAt         time.Time
}
