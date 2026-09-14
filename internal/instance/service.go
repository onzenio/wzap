// Package instance implements the instance management service: creation,
// lookup, paginated listing, partial updates and removal of WhatsApp
// instances.
package instance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
	"wzap/internal/webhook"
)

// Errors reported by the service and mapped to HTTP status codes by the
// handler layer.
var (
	// ErrNotFound reports that the requested instance does not exist.
	ErrNotFound = errors.New("instance not found")
	// ErrExternalRefTaken reports that an instance external_ref is already in use.
	ErrExternalRefTaken = errors.New("external ref already taken")
	// ErrInvalidCursor reports that a list cursor is not a valid identifier.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrAlreadyConnected reports that a pairing request hit an instance whose
	// session is already connected.
	ErrAlreadyConnected = errors.New("instance already connected")
	// ErrOwnerRequired reports that Create received no owner. The handler
	// always resolves the owner before calling, so this is a programmer
	// error answered 500.
	ErrOwnerRequired = errors.New("owner is required")
	// ErrOwnerNotFound reports that the requested owner matches no user. The
	// handler maps it to 422: creation-input validation.
	ErrOwnerNotFound = errors.New("owner not found")
	// ErrNoAdmin reports that no admin user exists to own an instance created
	// without an explicit owner. It is only reachable pre-seed and the
	// handler maps it to 500: server state, not client error.
	ErrNoAdmin = errors.New("no admin user exists")
	// ErrInvalidWebhook reports that a webhook configuration failed
	// validation (non-HTTP(S) URL, HTTP outside loopback, or an unknown event
	// type). The handler maps it to 422 and the failed write persists
	// nothing.
	ErrInvalidWebhook = errors.New("invalid webhook config")
)

// ConnectResult is the outcome of a pairing request: the resulting status and,
// while pairing, the QR code and its validity.
type ConnectResult struct {
	Status      session.Status
	QRCode      string
	QRExpiresAt *time.Time
}

// MediaRemover deletes the media files and rows of an instance. It is declared
// here, at the consumer, and will be satisfied by the media package (Task 16).
type MediaRemover interface {
	DeleteByInstance(ctx context.Context, instanceID uuid.UUID) error
}

// CreateInput is the payload accepted by Create. Name and ExternalRef are the
// fields the REST contract exposes; OwnerUserID is resolved by the handler
// from the request scope and is required non-nil at this boundary. The webhook
// fields are pointers so an absent field is distinct from an explicit value:
// an absent URL stays unset, an absent enabled stays false, and absent events
// default to every canonical type.
type CreateInput struct {
	Name           string
	ExternalRef    string
	OwnerUserID    *uuid.UUID
	WebhookURL     *string
	WebhookEnabled *bool
	WebhookEvents  *[]string
}

// UpdateInput is a partial update: nil fields keep their stored value, while an
// explicit empty ExternalRef clears the reference. The webhook fields follow
// the same rule: an absent URL, enabled, or events pointer leaves the stored
// configuration untouched, while an explicit empty URL unsets it and an
// explicit empty events list clears the subscription (delivering nothing).
type UpdateInput struct {
	Name           *string
	ExternalRef    *string
	WebhookURL     *string
	WebhookEnabled *bool
	WebhookEvents  *[]string
}

// Service manages the lifecycle of WhatsApp instances over the instance
// repository, the session manager, the media remover, the user repository
// (owner validation and oldest-admin lookup) and the API key repository (hash
// persist).
type Service struct {
	repo     storage.InstanceRepository
	sessions session.Manager
	media    MediaRemover
	users    storage.UserRepository
	keys     storage.APIKeyRepository
}

// NewService builds the service over its dependencies. media may be nil until
// instance media exists (Task 16); Delete then skips media removal. users and
// keys are required for Create; a nil one fails Create with a 500-mapped
// error instead of panicking.
func NewService(repo storage.InstanceRepository, sessions session.Manager, media MediaRemover, users storage.UserRepository, keys storage.APIKeyRepository) *Service {
	return &Service{repo: repo, sessions: sessions, media: media, users: users, keys: keys}
}

// Create registers a new instance in the disconnected state owned by
// input.OwnerUserID and emits its instance API key. The owner must resolve to
// an existing user; only the key hash is persisted via the keys repository,
// so the plaintext key exists in memory only and is returned here exactly
// once — never in a column, log or error. When the key mint or the hash
// persist fails after the row was inserted, the row is deleted again so a
// client retry starts clean instead of colliding with the orphaned row.
func (s *Service) Create(ctx context.Context, input CreateInput) (*model.Instance, string, error) {
	if input.OwnerUserID == nil {
		return nil, "", fmt.Errorf("create instance: %w", ErrOwnerRequired)
	}
	// The webhook configuration is pure input validation and runs before any
	// database read or write, so a 422 never partially persists.
	rawURL := ""
	if input.WebhookURL != nil {
		rawURL = *input.WebhookURL
	}
	webhookURL, webhookEvents, err := webhook.ValidateConfig(rawURL, input.WebhookEvents)
	if err != nil {
		return nil, "", fmt.Errorf("create instance: %w (%v)", ErrInvalidWebhook, err)
	}
	webhookEnabled := false
	if input.WebhookEnabled != nil {
		webhookEnabled = *input.WebhookEnabled
	}
	if s.users == nil {
		return nil, "", fmt.Errorf("create instance: users repository is not configured")
	}
	if _, err := s.users.GetByID(ctx, *input.OwnerUserID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, "", fmt.Errorf("create instance: %w", ErrOwnerNotFound)
		}
		return nil, "", fmt.Errorf("create instance: get owner: %w", err)
	}
	if s.keys == nil {
		return nil, "", fmt.Errorf("create instance: keys repository is not configured")
	}

	instance := model.Instance{
		ID:             uuid.New(),
		Name:           input.Name,
		ExternalRef:    input.ExternalRef,
		Status:         string(session.StatusDisconnected),
		OwnerUserID:    input.OwnerUserID,
		WebhookURL:     webhookURL,
		WebhookEnabled: webhookEnabled,
		WebhookEvents:  webhookEvents,
	}

	created, err := s.repo.Create(ctx, instance)
	if err != nil {
		return nil, "", mapError("create instance", err)
	}

	key, hash, err := auth.MintAPIKey()
	if err != nil {
		return nil, "", s.deleteCreated(ctx, created.ID, fmt.Errorf("mint api key: %w", err))
	}
	if err := s.keys.SetHash(ctx, created.ID, hash); err != nil {
		return nil, "", s.deleteCreated(ctx, created.ID, fmt.Errorf("store key hash: %w", err))
	}
	return created, key, nil
}

// deleteCreated removes a just-inserted instance after a post-insert failure
// (key mint or hash persist). A failed compensation is loud: the returned
// error mentions both faults, mirroring the seed rollback.
func (s *Service) deleteCreated(ctx context.Context, id uuid.UUID, cause error) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("create instance: %v; compensating instance delete also failed: %w", cause, err)
	}
	return fmt.Errorf("create instance: %w", cause)
}

// OldestAdmin returns the id of the admin user with the earliest created_at.
// users.List arrives ordered by created_at, so the first role==admin row wins.
// It reports ErrNoAdmin when no admin user exists.
func (s *Service) OldestAdmin(ctx context.Context) (uuid.UUID, error) {
	if s.users == nil {
		return uuid.Nil, fmt.Errorf("resolve oldest admin: users repository is not configured")
	}
	users, err := s.users.List(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve oldest admin: %w", err)
	}
	for i := range users {
		if users[i].Role == "admin" {
			return users[i].ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("resolve oldest admin: %w", ErrNoAdmin)
}

// Get returns the instance with the given id or ErrNotFound.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*model.Instance, error) {
	instance, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError("get instance", err)
	}
	return instance, nil
}

// List returns a page of instances and the cursor of the next page, empty on
// the last page.
func (s *Service) List(ctx context.Context, limit int, cursor string) ([]model.Instance, string, error) {
	instances, next, err := s.repo.List(ctx, limit, cursor)
	if err != nil {
		return nil, "", mapError("list instances", err)
	}
	return instances, next, nil
}

// Update applies the fields present in input to the stored instance and
// returns the stored row.
func (s *Service) Update(ctx context.Context, id uuid.UUID, input UpdateInput) (*model.Instance, error) {
	instance, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError("update instance", err)
	}

	if input.Name != nil {
		instance.Name = *input.Name
	}
	if input.ExternalRef != nil {
		instance.ExternalRef = *input.ExternalRef
	}
	// Only the webhook fields present in the patch are validated and applied;
	// absent ones keep the stored configuration. Validation runs before the
	// write, so a 422 keeps the previous configuration.
	if input.WebhookURL != nil {
		normalized, err := webhook.ValidateURL(*input.WebhookURL)
		if err != nil {
			return nil, fmt.Errorf("update instance: %w (%v)", ErrInvalidWebhook, err)
		}
		instance.WebhookURL = normalized
	}
	if input.WebhookEnabled != nil {
		instance.WebhookEnabled = *input.WebhookEnabled
	}
	if input.WebhookEvents != nil {
		normalized, err := webhook.ValidateEvents(*input.WebhookEvents)
		if err != nil {
			return nil, fmt.Errorf("update instance: %w (%v)", ErrInvalidWebhook, err)
		}
		instance.WebhookEvents = normalized
	}

	updated, err := s.repo.Update(ctx, *instance)
	if err != nil {
		return nil, mapError("update instance", err)
	}
	return updated, nil
}

// Connect starts the pairing of a non-connected instance and answers the QR
// code with its validity. An instance whose session is already connected is a
// no-op that answers with its status and no QR, while an instance already
// pairing answers the current code instead of opening a second channel: both
// keep the operation idempotent. The pairing state is persisted before
// returning.
func (s *Service) Connect(ctx context.Context, id uuid.UUID) (ConnectResult, error) {
	instance, err := s.repo.Get(ctx, id)
	if err != nil {
		return ConnectResult{}, mapError("connect instance", err)
	}

	sess, err := s.sessionFor(ctx, instance)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("connect instance: create session: %w", err)
	}

	switch sess.Status() {
	case session.StatusConnected:
		slog.Debug("connect instance branch", "instance_id", id, "op", "connect",
			"branch", "already-connected", "status", string(session.StatusConnected))
		return ConnectResult{Status: session.StatusConnected}, nil
	case session.StatusPairing:
		result, err := pairingResult(ctx, sess)
		if err != nil {
			slog.Warn("connect instance failed", "instance_id", id, "op", "connect",
				"branch", "already-pairing", "error", err)
			return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
		}
		slog.Debug("connect instance branch", append([]any{"instance_id", id, "op", "connect",
			"branch", "already-pairing"}, pairingLogAttrs(result)...)...)
		return result, nil
	}

	_, qr, expiresAt, err := s.connectPairing(ctx, instance, sess, "connect")
	if err != nil {
		slog.Warn("connect instance failed", "instance_id", id, "op", "connect",
			"branch", "new-pairing", "error", err)
		return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
	}
	if qr == "" {
		// The instance has stored credentials: connecting it online needs no
		// pairing, and the event sink persists the connected status.
		slog.Debug("connect instance branch", "instance_id", id, "op", "connect",
			"branch", "stored-credentials", "status", string(session.StatusConnected))
		return ConnectResult{Status: session.StatusConnected}, nil
	}

	if err := s.markPairing(ctx, instance); err != nil {
		slog.Warn("connect instance failed", "instance_id", id, "op", "connect",
			"branch", "persist-pairing", "error", err)
		return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
	}
	slog.Debug("connect instance branch", "instance_id", id, "op", "connect",
		"branch", "new-pairing", "status", string(session.StatusPairing),
		"qr_present", true, "expires_at", expiresAt)
	return ConnectResult{Status: session.StatusPairing, QRCode: qr, QRExpiresAt: &expiresAt}, nil
}

// QR returns the current pairing QR code of a non-connected instance. It
// starts the pairing when none is active, so an expired code is replaced. A
// connected instance is a conflict: it has no QR to scan.
func (s *Service) QR(ctx context.Context, id uuid.UUID) (ConnectResult, error) {
	instance, err := s.repo.Get(ctx, id)
	if err != nil {
		return ConnectResult{}, mapError("get qr", err)
	}

	sess, err := s.sessionFor(ctx, instance)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("get qr: create session: %w", err)
	}

	switch sess.Status() {
	case session.StatusConnected:
		slog.Debug("qr instance branch", "instance_id", id, "op", "qr",
			"branch", "already-connected", "status", string(session.StatusConnected))
		return ConnectResult{}, fmt.Errorf("get qr: %w", ErrAlreadyConnected)
	case session.StatusPairing:
		result, err := pairingResult(ctx, sess)
		if err != nil {
			slog.Warn("qr instance failed", "instance_id", id, "op", "qr",
				"branch", "already-pairing", "error", err)
			return ConnectResult{}, fmt.Errorf("get qr: %w", err)
		}
		slog.Debug("qr instance branch", append([]any{"instance_id", id, "op", "qr",
			"branch", "already-pairing"}, pairingLogAttrs(result)...)...)
		return result, nil
	}

	_, qr, expiresAt, err := s.connectPairing(ctx, instance, sess, "qr")
	if err != nil {
		slog.Warn("qr instance failed", "instance_id", id, "op", "qr",
			"branch", "new-pairing", "error", err)
		return ConnectResult{}, fmt.Errorf("get qr: %w", err)
	}
	if qr == "" {
		// Stored credentials mean the instance is paired already; there is no
		// QR to hand out.
		slog.Debug("qr instance branch", "instance_id", id, "op", "qr",
			"branch", "stored-credentials", "status", string(session.StatusConnected))
		return ConnectResult{}, fmt.Errorf("get qr: %w", ErrAlreadyConnected)
	}

	if err := s.markPairing(ctx, instance); err != nil {
		slog.Warn("qr instance failed", "instance_id", id, "op", "qr",
			"branch", "persist-pairing", "error", err)
		return ConnectResult{}, fmt.Errorf("get qr: %w", err)
	}
	slog.Debug("qr instance branch", "instance_id", id, "op", "qr",
		"branch", "new-pairing", "status", string(session.StatusPairing),
		"qr_present", true, "expires_at", expiresAt)
	return ConnectResult{Status: session.StatusPairing, QRCode: qr, QRExpiresAt: &expiresAt}, nil
}

// pairingResult reads the current code of an open pairing.
func pairingResult(ctx context.Context, sess session.Session) (ConnectResult, error) {
	qr, expiresAt, err := sess.QR(ctx)
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{Status: session.StatusPairing, QRCode: qr, QRExpiresAt: &expiresAt}, nil
}

// pairingLogAttrs builds the safe result attributes for a pairing outcome:
// the status plus whether a QR code is present and its expiry. The QR bytes
// themselves are never logged.
func pairingLogAttrs(result ConnectResult) []any {
	attrs := []any{"status", string(result.Status), "qr_present", result.QRCode != ""}
	if result.QRExpiresAt != nil {
		attrs = append(attrs, "expires_at", *result.QRExpiresAt)
	}
	return attrs
}

// sessionFor returns the session of instance, resetting a pairing whose
// persisted device is gone. The credentials cannot be recovered, so the
// instance is treated as unpaired: the stale JID is cleared and a fresh device
// is built so the caller can pair again instead of failing forever.
func (s *Service) sessionFor(ctx context.Context, instance *model.Instance) (session.Session, error) {
	sess, err := s.sessions.Create(instance)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, session.ErrNoDevice) {
		return nil, err
	}
	return s.resetPairing(ctx, instance)
}

// resetPairing forgets the unrecoverable pairing of instance and returns a
// fresh session ready to pair again. Removing the session first keeps the
// manager consistent when a session with a deleted device is still registered.
func (s *Service) resetPairing(ctx context.Context, instance *model.Instance) (session.Session, error) {
	slog.Debug("reset stale pairing", "instance_id", instance.ID, "branch", "reset-stale-device")
	if err := s.sessions.Remove(ctx, instance.ID); err != nil {
		return nil, fmt.Errorf("reset pairing: remove session: %w", err)
	}
	if err := s.repo.SetConnection(ctx, instance.ID, string(session.StatusDisconnected), ""); err != nil {
		return nil, mapError("reset pairing", err)
	}
	instance.WhatsAppJID = ""
	return s.sessions.Create(instance)
}

// connectPairing calls Session.Connect, resetting a pairing whose device was
// deleted underneath the session (an external logout) and retrying once. The
// op names the caller (connect or qr) for the boundary logs; error outcomes —
// including a context cancellation from Connect — are returned unchanged for
// the caller to log, so retry semantics are untouched.
func (s *Service) connectPairing(ctx context.Context, instance *model.Instance, sess session.Session, op string) (session.Session, string, time.Time, error) {
	qr, expiresAt, err := sess.Connect(ctx)
	if !errors.Is(err, session.ErrNoDevice) {
		return sess, qr, expiresAt, err
	}

	slog.Debug("connect pairing device gone, resetting stale pairing",
		"instance_id", instance.ID, "op", op, "branch", "reset-stale-device")
	sess, err = s.resetPairing(ctx, instance)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	qr, expiresAt, err = sess.Connect(ctx)
	return sess, qr, expiresAt, err
}

// markPairing persists the pairing state of instance so the status endpoint
// reflects it and a restart can resume it. The write only touches the
// connection columns: a full-row update could resurrect a stale last_error set
// by a concurrent connection event.
func (s *Service) markPairing(ctx context.Context, instance *model.Instance) error {
	if err := s.repo.SetConnection(ctx, instance.ID, string(session.StatusPairing), instance.WhatsAppJID); err != nil {
		return mapError("update instance", err)
	}
	return nil
}

// Restore brings the persisted sessions back online at startup. The manager
// bounds the concurrency and reports each outcome through the connection
// events, which keep instances.status in sync; only listing the instances can
// fail the call.
func (s *Service) Restore(ctx context.Context) error {
	if err := s.sessions.RestoreAll(ctx); err != nil {
		return fmt.Errorf("restore sessions: %w", err)
	}
	return nil
}

// Disconnect ends the session of an instance definitively: it disconnects the
// session, clears its paired identity and connection state, and deletes the
// stored credentials so the instance cannot be brought back online without a
// new pairing. An instance whose device is already gone is treated as
// disconnected: the stale JID is cleared and 204 is answered. The session emits
// the connection event that records the transition; the partial update keeps
// other columns untouched. On a credential removal failure the row is already
// cleared, so retrying finishes the removal.
func (s *Service) Disconnect(ctx context.Context, id uuid.UUID) error {
	instance, err := s.repo.Get(ctx, id)
	if err != nil {
		return mapError("disconnect instance", err)
	}

	sess, err := s.sessionFor(ctx, instance)
	if err != nil {
		return fmt.Errorf("disconnect instance: create session: %w", err)
	}
	if err := sess.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnect instance: %w", err)
	}
	if err := s.repo.SetConnection(ctx, id, string(session.StatusDisconnected), ""); err != nil {
		return mapError("disconnect instance", err)
	}
	if err := s.sessions.Remove(ctx, id); err != nil {
		return fmt.Errorf("disconnect instance: remove session: %w", err)
	}
	return nil
}

// Delete tears the session down, deletes the instance media and finally removes
// the row. A failure aborts before the row is removed, keeping the instance
// available so the removal can be retried instead of leaking credentials or
// media.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return mapError("delete instance", err)
	}
	if err := s.sessions.Remove(ctx, id); err != nil {
		return fmt.Errorf("delete instance: remove session: %w", err)
	}
	if s.media != nil {
		if err := s.media.DeleteByInstance(ctx, id); err != nil {
			return fmt.Errorf("delete instance: delete media: %w", err)
		}
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapError("delete instance", err)
	}
	return nil
}

// mapError translates a storage error into the service sentinel the HTTP layer
// maps to a status code, preserving the operation context for the logs.
func mapError(op string, err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case errors.Is(err, storage.ErrExternalRefTaken):
		return fmt.Errorf("%s: %w", op, ErrExternalRefTaken)
	case errors.Is(err, storage.ErrInvalidCursor):
		return fmt.Errorf("%s: %w", op, ErrInvalidCursor)
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
