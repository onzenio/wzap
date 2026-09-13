// Package instance implements the instance management service: creation,
// lookup, paginated listing, partial updates and removal of WhatsApp
// instances.
package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
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
// only fields the REST contract exposes.
type CreateInput struct {
	Name        string
	ExternalRef string
}

// UpdateInput is a partial update: nil fields keep their stored value, while an
// explicit empty ExternalRef clears the reference.
type UpdateInput struct {
	Name        *string
	ExternalRef *string
}

// Service manages the lifecycle of WhatsApp instances over the instance
// repository, the session manager and the media remover.
type Service struct {
	repo     storage.InstanceRepository
	sessions session.Manager
	media    MediaRemover
}

// NewService builds the service over its dependencies. media may be nil until
// instance media exists (Task 16); Delete then skips media removal.
func NewService(repo storage.InstanceRepository, sessions session.Manager, media MediaRemover) *Service {
	return &Service{repo: repo, sessions: sessions, media: media}
}

// Create registers a new instance in the disconnected state.
func (s *Service) Create(ctx context.Context, input CreateInput) (*model.Instance, error) {
	instance := model.Instance{
		ID:          uuid.New(),
		Name:        input.Name,
		ExternalRef: input.ExternalRef,
		Status:      string(session.StatusDisconnected),
	}

	created, err := s.repo.Create(ctx, instance)
	if err != nil {
		return nil, mapError("create instance", err)
	}
	return created, nil
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
		return ConnectResult{Status: session.StatusConnected}, nil
	case session.StatusPairing:
		result, err := pairingResult(ctx, sess)
		if err != nil {
			return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
		}
		return result, nil
	}

	_, qr, expiresAt, err := s.connectPairing(ctx, instance, sess)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
	}
	if qr == "" {
		// The instance has stored credentials: connecting it online needs no
		// pairing, and the event sink persists the connected status.
		return ConnectResult{Status: session.StatusConnected}, nil
	}

	if err := s.markPairing(ctx, instance); err != nil {
		return ConnectResult{}, fmt.Errorf("connect instance: %w", err)
	}
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
		return ConnectResult{}, fmt.Errorf("get qr: %w", ErrAlreadyConnected)
	case session.StatusPairing:
		result, err := pairingResult(ctx, sess)
		if err != nil {
			return ConnectResult{}, fmt.Errorf("get qr: %w", err)
		}
		return result, nil
	}

	_, qr, expiresAt, err := s.connectPairing(ctx, instance, sess)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("get qr: %w", err)
	}
	if qr == "" {
		// Stored credentials mean the instance is paired already; there is no
		// QR to hand out.
		return ConnectResult{}, fmt.Errorf("get qr: %w", ErrAlreadyConnected)
	}

	if err := s.markPairing(ctx, instance); err != nil {
		return ConnectResult{}, fmt.Errorf("get qr: %w", err)
	}
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
// deleted underneath the session (an external logout) and retrying once.
func (s *Service) connectPairing(ctx context.Context, instance *model.Instance, sess session.Session) (session.Session, string, time.Time, error) {
	qr, expiresAt, err := sess.Connect(ctx)
	if !errors.Is(err, session.ErrNoDevice) {
		return sess, qr, expiresAt, err
	}

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
