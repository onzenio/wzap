// Package storage declares the persistence contracts consumed by the wzap
// services.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
)

var (
	// ErrNotFound reports that the requested record does not exist.
	ErrNotFound = errors.New("record not found")
	// ErrExternalRefTaken reports that an instance external_ref is already in use.
	ErrExternalRefTaken = errors.New("external ref already taken")
	// ErrEmailTaken reports that a user email is already in use
	// (case-insensitive, matching the lower(email) unique index).
	ErrEmailTaken = errors.New("email already taken")
	// ErrInvalidCursor reports that a pagination cursor is not a valid identifier.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrFingerprintMismatch reports that an idempotency key was reused with a
	// different request fingerprint.
	ErrFingerprintMismatch = errors.New("idempotency fingerprint mismatch")
	// ErrInProgress reports that an idempotency key is owned by a request that
	// has not completed yet.
	ErrInProgress = errors.New("idempotency key in progress")
)

// InstanceRepository persists WhatsApp instances. List pages backwards by
// created_at and returns the cursor of the next page, empty when the page is
// the last one.
type InstanceRepository interface {
	Create(ctx context.Context, instance model.Instance) (*model.Instance, error)
	Get(ctx context.Context, id uuid.UUID) (*model.Instance, error)
	GetByExternalRef(ctx context.Context, externalRef string) (*model.Instance, error)
	List(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error)
	Update(ctx context.Context, instance model.Instance) (*model.Instance, error)
	// SetConnection updates the status and whatsapp_jid of an instance in
	// place, clearing the JID when whatsappJID is empty. Unlike Update it never
	// touches the other columns, so a concurrent writer cannot be overwritten
	// with stale values.
	SetConnection(ctx context.Context, id uuid.UUID, status, whatsappJID string) error
	// SetConnectionState records a connection transition in place: status and
	// last_error always, whatsapp_jid when it is not empty (keeping the stored
	// one otherwise) and last_connected_at when connectedAt is set. Like
	// SetConnection it never touches the other columns, so a concurrent
	// writer is not overwritten with stale values.
	SetConnectionState(ctx context.Context, id uuid.UUID, status, whatsappJID, lastError string, connectedAt *time.Time) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// MessageRepository persists outbound messages. ListByInstance pages backwards
// by created_at and returns the cursor of the next page, empty when the page is
// the last one.
type MessageRepository interface {
	Create(ctx context.Context, message model.OutboundMessage) (*model.OutboundMessage, error)
	Get(ctx context.Context, id uuid.UUID) (*model.OutboundMessage, error)
	ListByInstance(ctx context.Context, instanceID uuid.UUID, limit int, cursor string) ([]model.OutboundMessage, string, error)
	// ClaimQueued selects queued messages whose next_attempt_at is due, oldest
	// first, and atomically moves them to sending with FOR UPDATE SKIP LOCKED.
	ClaimQueued(ctx context.Context, limit int) ([]model.OutboundMessage, error)
	MarkSent(ctx context.Context, id uuid.UUID, whatsAppMessageID string) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
	// MarkRetrying moves a message back to queued, incrementing attempts.
	MarkRetrying(ctx context.Context, id uuid.UUID, errMsg string, nextAttemptAt time.Time) error
	// UpdateReceipt maps delivered to delivered_at and read/played to read_at.
	// It reports false when no message matches or the status is unknown.
	UpdateReceipt(ctx context.Context, whatsAppMessageID, status string, at time.Time) (bool, error)
	// RequeueStuck moves sending messages updated before olderThan back to queued.
	RequeueStuck(ctx context.Context, olderThan time.Time) (int64, error)
}

// MediaRepository persists media metadata. The content itself lives on the
// filesystem: StoragePath locates it relative to the configured data dir.
// ListByInstance and ListExpired return the rows so the caller can delete the
// files before removing the records.
type MediaRepository interface {
	// Create persists media with the provided ExpiresAt and returns the stored
	// row with the database created_at. It returns ErrNotFound when the
	// instance does not exist.
	Create(ctx context.Context, media model.Media) (*model.Media, error)
	// Get returns the media with the given id or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*model.Media, error)
	// ListByInstance returns every media of instanceID ordered by created_at.
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]model.Media, error)
	// ListExpired returns the media whose expires_at is due, oldest first.
	ListExpired(ctx context.Context, now time.Time) ([]model.Media, error)
	// Delete removes one media row. It returns ErrNotFound when it is absent.
	Delete(ctx context.Context, id uuid.UUID) error
	// DeleteByInstance removes every media row of instanceID.
	DeleteByInstance(ctx context.Context, instanceID uuid.UUID) (int64, error)
}

// IdempotencyRepository persists request outcomes keyed by
// (instance, Idempotency-Key).
type IdempotencyRepository interface {
	// Acquire tries to own key. It returns acquired true with the fresh record
	// when it won the key. A key that expired is treated as absent and
	// reacquired. When another request already completed the key, it returns
	// the stored record with acquired false. Reusing a key with a different
	// fingerprint returns ErrFingerprintMismatch; an in-flight key returns
	// ErrInProgress.
	Acquire(ctx context.Context, instanceID uuid.UUID, key, fingerprint string, expiresAt time.Time) (*model.IdempotencyRecord, bool, error)
	// Complete stores the original response under key. It returns ErrNotFound
	// when the key does not exist.
	Complete(ctx context.Context, instanceID uuid.UUID, key string, status int, body []byte) error
	// Release frees key so a corrected request can retry under it. Releasing an
	// absent key is a no-op.
	Release(ctx context.Context, instanceID uuid.UUID, key string) error
	// DeleteExpired removes keys whose expires_at is in the past.
	DeleteExpired(ctx context.Context) (int64, error)
}

// JIDCacheRepository persists phone to JID resolutions until their expiry.
type JIDCacheRepository interface {
	// Get returns the cached JID for phone. Expired entries count as a miss.
	Get(ctx context.Context, phone string) (jid string, ok bool, err error)
	// Put upserts the JID resolved for phone.
	Put(ctx context.Context, phone, jid string, expiresAt time.Time) error
	// DeleteExpired removes entries whose expires_at is in the past.
	DeleteExpired(ctx context.Context) (int64, error)
}

// EventOutboxRepository persists events until the relay publishes them.
type EventOutboxRepository interface {
	Enqueue(ctx context.Context, id uuid.UUID, subject string, envelope []byte) error
	// ClaimPending returns up to limit unpublished events, oldest first.
	ClaimPending(ctx context.Context, limit int) ([]model.OutboxEvent, error)
	// MarkPublished stamps the event as published. It returns ErrNotFound when
	// the event does not exist.
	MarkPublished(ctx context.Context, id uuid.UUID) error
	// MarkAttempt records a failed publish attempt. It returns ErrNotFound when
	// the event does not exist.
	MarkAttempt(ctx context.Context, id uuid.UUID, errMsg string) error
	// DeletePublishedBefore removes published events stamped before t.
	DeletePublishedBefore(ctx context.Context, t time.Time) (int64, error)
}

// UserRepository persists manager users. GetByEmail matches case-insensitively
// and Create reports ErrEmailTaken when the email is already in use.
type UserRepository interface {
	Create(ctx context.Context, user model.User) (*model.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	// List returns every user ordered by created_at.
	List(ctx context.Context) ([]model.User, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Count(ctx context.Context) (int, error)
	// UpdateQuota stores a new per-user instance quota. A stored 0 means
	// unlimited. It reports ErrNotFound when the user does not exist.
	UpdateQuota(ctx context.Context, id uuid.UUID, quota int) error
}

// APIKeyRepository manages the instance key hashes used to authenticate
// instance-scoped requests. Hashes never leave the repository: callers pass
// hashes in and get instance ids back.
type APIKeyRepository interface {
	// SetHash stores (or replaces) the key hash of an instance. It returns
	// ErrNotFound when the instance does not exist.
	SetHash(ctx context.Context, instanceID uuid.UUID, hash string) error
	// InstanceByHash resolves the instance id holding hash or ErrNotFound.
	InstanceByHash(ctx context.Context, hash string) (uuid.UUID, error)
	// ClearHash revokes the instance key by NULLing its hash. It returns
	// ErrNotFound when the instance does not exist.
	ClearHash(ctx context.Context, instanceID uuid.UUID) error
	// CountByOwner counts the instances owned by owner. CountAll counts every
	// instance, any state; both feed the quota checks.
	CountByOwner(ctx context.Context, owner uuid.UUID) (int, error)
	CountAll(ctx context.Context) (int, error)
}

// ChatwootConfigRepository persists the per-instance Chatwoot connector
// configuration. Get reports ErrNotFound when the instance was never
// configured.
type ChatwootConfigRepository interface {
	Get(ctx context.Context, instanceID uuid.UUID) (*model.ChatwootConfig, error)
	Put(ctx context.Context, cfg model.ChatwootConfig) (*model.ChatwootConfig, error)
	Delete(ctx context.Context, instanceID uuid.UUID) error
}

// ChatwootMessageRepository persists WhatsApp to Chatwoot ID correlations.
// Rows are never purged; DeleteByInstance removes every row of an instance.
type ChatwootMessageRepository interface {
	Put(ctx context.Context, msg model.ChatwootMessage) (*model.ChatwootMessage, error)
	GetByWAKey(ctx context.Context, instanceID uuid.UUID, waKey string) (*model.ChatwootMessage, error)
	DeleteByInstance(ctx context.Context, instanceID uuid.UUID) (int64, error)
}
