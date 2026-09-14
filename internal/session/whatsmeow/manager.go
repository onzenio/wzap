// Package whatsmeow implements the session contracts on top of the upstream
// go.mau.fi/whatsmeow library. All library types stay confined to this package.
package whatsmeow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

const (
	// restoreConcurrency limits how many persisted sessions reconnect at once.
	restoreConcurrency = 2
	// restoreJitter spreads reconnections so they do not hit the server
	// together.
	restoreJitter = 500 * time.Millisecond
	// instancePageSize is the page size used to walk the instances table.
	instancePageSize = 100
)

// Manager owns the whatsmeow client of every instance and persists the
// sessions in the Postgres device store.
type Manager struct {
	devices       *sqlstore.Container
	instances     storage.InstanceRepository
	log           *slog.Logger
	sink          session.EventSink
	maxMediaBytes int64

	// restoreConnect brings a restored session online; tests replace it to
	// avoid the network handshake.
	restoreConnect func(ctx context.Context, sess *instanceSession) error

	mu       sync.RWMutex
	sessions map[uuid.UUID]*instanceSession
}

var _ session.Manager = (*Manager)(nil)

// NewManager opens the whatsmeow device store in the Postgres database
// addressed by databaseURL. instances lets RestoreAll map persisted devices
// back to their instance, sink receives the session events and maxMediaBytes
// caps how much inbound media a download may buffer.
func NewManager(ctx context.Context, databaseURL string, instances storage.InstanceRepository, log *slog.Logger, sink session.EventSink, maxMediaBytes int64) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	devices, err := openDeviceStore(ctx, databaseURL, log)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		devices:       devices,
		instances:     instances,
		log:           log,
		sink:          sink,
		maxMediaBytes: maxMediaBytes,
		sessions:      make(map[uuid.UUID]*instanceSession),
	}
	manager.restoreConnect = func(ctx context.Context, sess *instanceSession) error {
		return sess.connectExisting(ctx)
	}
	return manager, nil
}

// Close releases the whatsmeow database connections.
func (m *Manager) Close() error {
	if m == nil || m.devices == nil {
		return nil
	}
	return m.devices.Close()
}

// Get returns the session of instanceID when it is known.
func (m *Manager) Get(instanceID uuid.UUID) (session.Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[instanceID]
	if !ok {
		return nil, false
	}
	return sess, true
}

// Create returns the session of instance, loading the persisted device when
// the instance already has a whatsapp_jid and building a fresh device for a
// new pairing otherwise. Repeating it for a known instance returns the
// existing session.
func (m *Manager) Create(instance *model.Instance) (session.Session, error) {
	if instance == nil {
		return nil, errors.New("create session: nil instance")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[instance.ID]; ok {
		return sess, nil
	}

	device, err := m.deviceFor(context.Background(), instance)
	if err != nil {
		return nil, err
	}
	sess, err := newSession(instance.ID, device, m.log, m.sink, m.maxMediaBytes)
	if err != nil {
		return nil, err
	}
	m.sessions[instance.ID] = sess
	return sess, nil
}

// Remove disconnects the session and deletes its stored credentials. When the
// deletion fails the session stays registered, so a retry can finish removing
// the credentials instead of silently reporting success.
func (m *Manager) Remove(ctx context.Context, instanceID uuid.UUID) error {
	m.mu.Lock()
	sess, ok := m.sessions[instanceID]
	if ok {
		delete(m.sessions, instanceID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}

	if err := sess.remove(ctx); err != nil {
		m.mu.Lock()
		if _, exists := m.sessions[instanceID]; !exists {
			m.sessions[instanceID] = sess
		}
		m.mu.Unlock()
		return err
	}
	return nil
}

// RestoreAll reconnects every persisted session, at most restoreConcurrency at
// a time and with jitter. Failures are reported per instance through the event
// sink; only listing the instances can fail the call.
func (m *Manager) RestoreAll(ctx context.Context) error {
	if m.instances == nil {
		return errors.New("restore sessions: instance repository not configured")
	}
	instances, err := m.listInstances(ctx)
	if err != nil {
		return err
	}

	sem := make(chan struct{}, restoreConcurrency)
	var wg sync.WaitGroup
	for _, instance := range instances {
		if instance.WhatsAppJID == "" {
			continue
		}
		if _, ok := m.Get(instance.ID); ok {
			continue
		}
		wg.Add(1)
		go func(instance model.Instance) {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				m.restoreAborted(instance, err)
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				m.restoreAborted(instance, ctx.Err())
				return
			}
			defer func() { <-sem }()

			if err := sleepCtx(ctx, time.Duration(rand.Int64N(int64(restoreJitter)))); err != nil {
				m.restoreAborted(instance, err)
				return
			}
			if err := m.restore(ctx, instance); err != nil {
				m.log.Warn("restore session failed", "instance_id", instance.ID, "jid", instance.WhatsAppJID, "error", err)
				m.emitConnection(instance.ID, session.StatusError, instance.WhatsAppJID, err.Error())
			}
		}(instance)
	}
	wg.Wait()
	return nil
}

// restoreAborted reports a restore that the context ended before it could run,
// so the instance status reflects that it was not restored.
func (m *Manager) restoreAborted(instance model.Instance, err error) {
	reason := "restore cancelled: " + err.Error()
	m.log.Warn("restore session cancelled", "instance_id", instance.ID, "jid", instance.WhatsAppJID, "error", err)
	m.emitConnection(instance.ID, session.StatusError, instance.WhatsAppJID, reason)
}

// restore attaches the persisted device of instance and brings it online.
func (m *Manager) restore(ctx context.Context, instance model.Instance) error {
	jid, err := types.ParseJID(instance.WhatsAppJID)
	if err != nil {
		return fmt.Errorf("parse jid %q: %w", instance.WhatsAppJID, err)
	}
	device, err := m.devices.GetDevice(ctx, jid)
	if err != nil {
		return fmt.Errorf("load device %s: %w", jid, err)
	}
	if device == nil {
		return fmt.Errorf("device %s: %w", jid, session.ErrNoDevice)
	}
	return m.attachAndConnect(ctx, instance.ID, device)
}

// attachAndConnect registers a session built from device and brings it online.
// A concurrent Create or RestoreAll that wins the registration turns this into
// a no-op, so a client that is not in the manager is never connected: two live
// clients on the same device would fight over the session.
func (m *Manager) attachAndConnect(ctx context.Context, instanceID uuid.UUID, device *store.Device) error {
	sess, err := newSession(instanceID, device, m.log, m.sink, m.maxMediaBytes)
	if err != nil {
		return err
	}
	if !m.registerRestored(instanceID, sess) {
		return nil
	}
	connect := m.restoreConnect
	if connect == nil {
		connect = func(ctx context.Context, sess *instanceSession) error {
			return sess.connectExisting(ctx)
		}
	}
	return connect(ctx, sess)
}

// registerRestored stores sess for instanceID unless another goroutine already
// registered a session, reporting whether this call performed the insert.
func (m *Manager) registerRestored(instanceID uuid.UUID, sess *instanceSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[instanceID]; ok {
		return false
	}
	m.sessions[instanceID] = sess
	return true
}

// listInstances walks the instances table page by page.
func (m *Manager) listInstances(ctx context.Context) ([]model.Instance, error) {
	var all []model.Instance
	cursor := ""
	for {
		page, next, err := m.instances.List(ctx, instancePageSize, cursor)
		if err != nil {
			return nil, fmt.Errorf("list instances: %w", err)
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}

// deviceFor returns the device store of instance, creating a fresh one when
// the instance was never paired.
func (m *Manager) deviceFor(ctx context.Context, instance *model.Instance) (*store.Device, error) {
	if instance.WhatsAppJID == "" {
		return m.devices.NewDevice(), nil
	}
	jid, err := types.ParseJID(instance.WhatsAppJID)
	if err != nil {
		return nil, fmt.Errorf("create session: parse jid %q: %w", instance.WhatsAppJID, err)
	}
	device, err := m.devices.GetDevice(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("create session: load device %s: %w", jid, err)
	}
	if device == nil {
		return nil, fmt.Errorf("create session: device %s: %w", jid, session.ErrNoDevice)
	}
	return device, nil
}

// emitConnection reports a connection change for an instance with no session
// (used when restoring fails before a session exists).
func (m *Manager) emitConnection(instanceID uuid.UUID, status session.Status, jid, reason string) {
	if m.sink == nil {
		return
	}
	m.sink.OnConnection(context.Background(), instanceID, status, jid, reason)
}

// sleepCtx waits for d unless the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// instanceSession is the session.Session implementation for one instance.
type instanceSession struct {
	instanceID    uuid.UUID
	client        *whatsmeow.Client
	sink          session.EventSink
	log           *slog.Logger
	maxMediaBytes int64

	// sleep and backoff drive the auto-reconnect; tests replace them to assert
	// the retry transitions without real waits.
	sleep   sleepFunc
	backoff reconnectPolicy
	// reconnectFn brings the session online during an auto-reconnect. It
	// defaults to connectExisting and is replaced in the transition tests.
	reconnectFn func(ctx context.Context) error
	// logoutFn removes the companion device from WhatsApp. It defaults to the
	// client Logout and is replaced in the tests to assert the attempt without
	// a network handshake.
	logoutFn func(ctx context.Context) error
	// sendRevokeFn sends a revoke protocol message. It defaults to SendMessage
	// and is replaced in the tests to assert the built revocation without a
	// network round-trip.
	sendRevokeFn func(ctx context.Context, chat types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error)
	// markReadFn sends a read receipt. It defaults to the client MarkRead and
	// is replaced in the tests to assert the receipt arguments.
	markReadFn func(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID) error
	// pairPhoneFn requests a phone pairing code. It defaults to the client
	// PairPhone and is replaced in the tests to avoid the pairing handshake.
	pairPhoneFn func(ctx context.Context, phone string) (string, error)

	// history accumulates the per-instance history-sync feed the Import plan
	// consumes. Each session owns one, so feeds never cross instance
	// boundaries. The zero value is ready to use.
	history session.HistorySyncAccumulator

	mu           sync.RWMutex
	status       session.Status
	jid          string
	paired       bool
	terminal     bool
	qrCode       string
	qrExpiresAt  time.Time
	firstQR      chan qrResult
	qrCancel     context.CancelFunc
	lastReason   string
	reconnectRun *reconnectRun
}

var _ session.Session = (*instanceSession)(nil)

// qrResult carries the outcome of a pairing attempt to the caller waiting in
// Connect.
type qrResult struct {
	code      string
	expiresAt time.Time
	err       error
}

// newSession wraps device in a connected-aware session and registers the event
// translation. maxMediaBytes caps how much inbound media a download may
// buffer.
func newSession(instanceID uuid.UUID, device *store.Device, log *slog.Logger, sink session.EventSink, maxMediaBytes int64) (*instanceSession, error) {
	if device == nil {
		return nil, errors.New("new session: nil device")
	}
	if log == nil {
		log = slog.Default()
	}
	sess := &instanceSession{
		instanceID:    instanceID,
		sink:          sink,
		log:           log,
		maxMediaBytes: maxMediaBytes,
		status:        session.StatusDisconnected,
		sleep:         sleepCtx,
		backoff: reconnectPolicy{
			base:   reconnectBaseDelay,
			max:    reconnectMaxDelay,
			jitter: defaultJitter,
		},
	}
	client := whatsmeow.NewClient(device, newWALogger(log))
	// wzap drives its own exponential backoff; the library reconnect would
	// otherwise race it with a linear, uncapped schedule.
	client.EnableAutoReconnect = false
	if device.ID != nil && !device.ID.IsEmpty() {
		sess.jid = device.ID.String()
		sess.paired = true
	}
	client.AddEventHandler(sess.dispatch)
	sess.client = client
	sess.reconnectFn = sess.connectExisting
	return sess, nil
}

// Status returns the current lifecycle state.
func (s *instanceSession) Status() session.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// JID returns the public JID, empty while the instance is not paired.
func (s *instanceSession) JID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jid
}

// setStatus updates the lifecycle state, reports it to the sink when it
// changed and applies the reconnect policy of the transition. A paired JID
// marks the session as reconnectable, so a connected event after a pairing
// turns later drops into retries. Terminal states (error and every
// disconnected transition carrying a reason) stick: the socket close that
// follows a restriction or a logout must not turn it into a retry.
func (s *instanceSession) setStatus(status session.Status, jid, reason string) {
	s.mu.Lock()
	if status == session.StatusDisconnected && reason == "" && s.terminal {
		s.mu.Unlock()
		return
	}
	if s.status == status && s.lastReason == reason {
		s.mu.Unlock()
		return
	}
	s.status = status
	s.lastReason = reason
	s.terminal = status == session.StatusError || (status == session.StatusDisconnected && reason != "")
	if jid != "" {
		s.jid = jid
		s.paired = true
	}
	currentJID := s.jid
	s.mu.Unlock()

	if s.sink != nil {
		s.sink.OnConnection(context.Background(), s.instanceID, status, currentJID, reason)
	}
	s.applyConnectionPolicy(status, reason)
}

// Send delivers an outbound message through the whatsmeow client.
func (s *instanceSession) Send(ctx context.Context, msg session.OutboundMessage) (string, error) {
	if !s.client.IsConnected() {
		return "", fmt.Errorf("%w: send %s", session.ErrNotConnected, msg.Type)
	}
	recipient, err := types.ParseJID(msg.RecipientJID)
	if err != nil {
		return "", fmt.Errorf("%w: %s", session.ErrInvalidRecipient, msg.RecipientJID)
	}

	var waMsg *waE2E.Message
	if isMediaType(msg.Type) {
		waMsg, err = s.uploadMedia(ctx, msg)
	} else {
		waMsg, err = buildMessage(msg)
	}
	if err != nil {
		return "", err
	}

	resp, err := s.client.SendMessage(ctx, recipient, waMsg)
	if err != nil {
		return "", classifySessionError(err)
	}
	return string(resp.ID), nil
}

// IsOnWhatsApp resolves a phone number to its canonical JID.
func (s *instanceSession) IsOnWhatsApp(ctx context.Context, phone string) (string, bool, error) {
	if !s.client.IsConnected() {
		return "", false, fmt.Errorf("%w: is on whatsapp", session.ErrNotConnected)
	}
	responses, err := s.client.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		return "", false, classifySessionError(err)
	}
	if len(responses) == 0 || !responses[0].IsIn || responses[0].JID.IsEmpty() {
		return "", false, nil
	}
	return responses[0].JID.String(), true, nil
}

// presenceRequest is a parsed SendPresence state.
type presenceRequest struct {
	isChat bool
	chat   types.ChatPresence
	user   types.Presence
}

// parsePresence classifies the presence states accepted by SendPresence.
func parsePresence(state string) (presenceRequest, error) {
	switch types.ChatPresence(state) {
	case types.ChatPresenceComposing, types.ChatPresencePaused:
		return presenceRequest{isChat: true, chat: types.ChatPresence(state)}, nil
	}
	switch types.Presence(state) {
	case types.PresenceAvailable, types.PresenceUnavailable:
		return presenceRequest{user: types.Presence(state)}, nil
	}
	return presenceRequest{}, fmt.Errorf("unsupported presence state %q", state)
}

// SendPresence reports chat typing or user availability.
func (s *instanceSession) SendPresence(ctx context.Context, chatJID, state string) error {
	target, err := parsePresence(state)
	if err != nil {
		return err
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("%w: %s", session.ErrInvalidRecipient, chatJID)
	}
	if target.isChat {
		return s.client.SendChatPresence(ctx, jid, target.chat, types.ChatPresenceMediaText)
	}
	return s.client.SendPresence(ctx, target.user)
}

// DeleteMessage revokes a sent message for everyone in the chat. The pinned
// library deprecated RevokeMessage in favor of BuildRevoke + SendMessage, so
// the session builds the REVOKE protocol message keyed by the original id and
// sends it to the chat. Connectivity failures surface through the shared
// classifier (not-connected vs transient).
func (s *instanceSession) DeleteMessage(ctx context.Context, chatJID, messageID string) error {
	// ParseJID in the pinned library is lenient (it only splits user/server),
	// so an empty result is rejected explicitly; anything else parses like
	// Send does.
	chat, err := types.ParseJID(chatJID)
	if err != nil || chat.IsEmpty() {
		return fmt.Errorf("%w: %s", session.ErrInvalidRecipient, chatJID)
	}
	if messageID == "" {
		return errors.New("delete message: empty message id")
	}
	send := s.sendRevokeFn
	if send == nil {
		send = func(ctx context.Context, chat types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
			return s.client.SendMessage(ctx, chat, msg)
		}
	}
	if _, err := send(ctx, chat, s.client.BuildRevoke(chat, types.EmptyJID, types.MessageID(messageID))); err != nil {
		return classifySessionError(err)
	}
	return nil
}

// MarkRead sends a read receipt for one message. An empty senderJID falls
// back to the chat JID (direct chats); group reads carry the author, as the
// library requires. The receipt is stamped at read time.
func (s *instanceSession) MarkRead(ctx context.Context, chatJID, senderJID, messageID string) error {
	chat, err := types.ParseJID(chatJID)
	if err != nil || chat.IsEmpty() {
		return fmt.Errorf("%w: %s", session.ErrInvalidRecipient, chatJID)
	}
	sender := chat
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil || sender.IsEmpty() {
			return fmt.Errorf("%w: %s", session.ErrInvalidRecipient, senderJID)
		}
	}
	if messageID == "" {
		return errors.New("mark read: empty message id")
	}
	mark := s.markReadFn
	if mark == nil {
		mark = func(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID) error {
			return s.client.MarkRead(ctx, ids, timestamp, chat, sender)
		}
	}
	if err := mark(ctx, []types.MessageID{types.MessageID(messageID)}, time.Now().UTC(), chat, sender); err != nil {
		return classifySessionError(err)
	}
	return nil
}

// Disconnect asks WhatsApp to drop the companion device while the connection
// is still up, then closes it, stops any pending auto-reconnect and clears the
// paired identity of the session. A logout the server cannot be reached for is
// not fatal: the local teardown still completes.
func (s *instanceSession) Disconnect(ctx context.Context) error {
	s.cancelQR()
	s.cancelReconnect()
	s.logoutTolerantly(ctx)
	s.client.Disconnect()

	s.mu.Lock()
	s.jid = ""
	s.paired = false
	s.mu.Unlock()

	s.setStatus(session.StatusDisconnected, "", "disconnected by request")
	return nil
}

// remove asks WhatsApp to drop the companion device, disconnects the client and
// deletes its stored credentials. It is safe to call it after Disconnect, when
// the logout attempt only sees a dropped connection.
func (s *instanceSession) remove(ctx context.Context) error {
	s.cancelQR()
	s.cancelReconnect()
	s.logoutTolerantly(ctx)
	s.client.Disconnect()
	if s.client.Store.ID != nil {
		if err := s.client.Store.Delete(ctx); err != nil {
			return fmt.Errorf("delete device: %w", err)
		}
	}
	// A session already disconnected through the API emitted its own event;
	// only report the removal when it actually changed the state.
	if s.Status() != session.StatusDisconnected {
		s.setStatus(session.StatusDisconnected, "", "session removed")
	}
	return nil
}

// logout asks WhatsApp to remove the companion device, defaulting to the
// client Logout. Tests replace logoutFn to assert the attempt.
func (s *instanceSession) logout(ctx context.Context) error {
	if s.logoutFn != nil {
		return s.logoutFn(ctx)
	}
	if s.client == nil {
		return nil
	}
	return s.client.Logout(ctx)
}

// logoutTolerantly attempts the WhatsApp logout. A session that was never
// online and a device that is already gone are treated as done: the local
// teardown continues in every case and only unexpected failures are logged.
func (s *instanceSession) logoutTolerantly(ctx context.Context) {
	err := s.logout(ctx)
	switch {
	case err == nil,
		errors.Is(err, whatsmeow.ErrNotConnected),
		errors.Is(err, whatsmeow.ErrNotLoggedIn),
		errors.Is(err, store.ErrDeviceDeleted):
		return
	}
	s.log.Warn("logout from whatsapp failed", "instance_id", s.instanceID, "error", err)
}

// connectExisting brings an already paired device online.
func (s *instanceSession) connectExisting(ctx context.Context) error {
	if err := s.client.ConnectContext(ctx); err != nil {
		if errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			return nil
		}
		return classifySessionError(err)
	}
	return nil
}

// cancelQR stops the pairing channel of the session, when one is open.
func (s *instanceSession) cancelQR() {
	s.mu.Lock()
	cancel := s.qrCancel
	s.qrCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// classifySessionError maps a whatsmeow error onto the errors callers act on.
func classifySessionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, whatsmeow.ErrNotConnected) || errors.Is(err, whatsmeow.ErrNotLoggedIn) {
		return fmt.Errorf("%w: %v", session.ErrNotConnected, err)
	}
	return fmt.Errorf("%w: %v", session.ErrTransient, err)
}

// outboundPayload is the JSON body shared by every message type.
type outboundPayload struct {
	Text        string   `json:"text"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	DisplayName string   `json:"display_name"`
	VCard       string   `json:"vcard"`
	Caption     string   `json:"caption"`
	Filename    string   `json:"filename"`
	MimeType    string   `json:"mime_type"`
	PTT         bool     `json:"ptt"`
}

// buildMessage builds a non-media message from its normalized payload.
func buildMessage(msg session.OutboundMessage) (*waE2E.Message, error) {
	payload, err := decodePayload(msg.Payload)
	if err != nil {
		return nil, err
	}
	switch msg.Type {
	case "text":
		if payload.Text == "" {
			return nil, errors.New("text payload is empty")
		}
		return &waE2E.Message{Conversation: proto.String(payload.Text)}, nil
	case "location":
		if payload.Latitude == nil || payload.Longitude == nil {
			return nil, errors.New("location payload is missing coordinates")
		}
		return &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  payload.Latitude,
			DegreesLongitude: payload.Longitude,
			Name:             optionalString(payload.Name),
			Address:          optionalString(payload.Address),
		}}, nil
	case "contact":
		if payload.VCard == "" {
			return nil, errors.New("contact payload is missing the vcard")
		}
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
			DisplayName: optionalString(payload.DisplayName),
			Vcard:       proto.String(payload.VCard),
		}}, nil
	}
	return nil, fmt.Errorf("unsupported message type %q", msg.Type)
}

// newMediaMessage builds a media message from an uploaded attachment.
func newMediaMessage(msg session.OutboundMessage, upload whatsmeow.UploadResponse) (*waE2E.Message, error) {
	if !isMediaType(msg.Type) {
		return nil, fmt.Errorf("unsupported media type %q", msg.Type)
	}
	payload, err := decodePayload(msg.Payload)
	if err != nil {
		return nil, err
	}
	if payload.MimeType == "" {
		return nil, errors.New("media payload is missing the mime type")
	}
	switch msg.Type {
	case "image":
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileSHA256:    upload.FileSHA256,
			FileEncSHA256: upload.FileEncSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(payload.MimeType),
			Caption:       optionalString(payload.Caption),
		}}, nil
	case "video":
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileSHA256:    upload.FileSHA256,
			FileEncSHA256: upload.FileEncSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(payload.MimeType),
			Caption:       optionalString(payload.Caption),
		}}, nil
	case "audio":
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileSHA256:    upload.FileSHA256,
			FileEncSHA256: upload.FileEncSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(payload.MimeType),
			PTT:           proto.Bool(payload.PTT),
		}}, nil
	case "document":
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileSHA256:    upload.FileSHA256,
			FileEncSHA256: upload.FileEncSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Mimetype:      proto.String(payload.MimeType),
			Caption:       optionalString(payload.Caption),
			FileName:      optionalString(payload.Filename),
			Title:         optionalString(payload.Filename),
		}}, nil
	}
	return nil, fmt.Errorf("unsupported media type %q", msg.Type)
}

// uploadMedia reads the file of a media message and uploads it to WhatsApp.
func (s *instanceSession) uploadMedia(ctx context.Context, msg session.OutboundMessage) (*waE2E.Message, error) {
	data, err := os.ReadFile(msg.MediaPath)
	if err != nil {
		return nil, fmt.Errorf("read media %s: %w", msg.MediaPath, err)
	}
	upload, err := s.client.Upload(ctx, data, mediaTypeFor(msg.Type))
	if err != nil {
		return nil, classifySessionError(err)
	}
	return newMediaMessage(msg, upload)
}

// decodePayload parses the JSON body of an outbound message. An empty payload
// decodes to its zero value.
func decodePayload(raw []byte) (outboundPayload, error) {
	var payload outboundPayload
	if len(raw) == 0 {
		return payload, nil
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return outboundPayload{}, fmt.Errorf("decode payload: %w", err)
	}
	return payload, nil
}

// isMediaType reports whether type is delivered as an uploaded attachment.
func isMediaType(messageType string) bool {
	switch messageType {
	case "image", "video", "audio", "document":
		return true
	}
	return false
}

// mediaTypeFor maps a message type to the whatsmeow media key namespace.
func mediaTypeFor(messageType string) whatsmeow.MediaType {
	switch messageType {
	case "image":
		return whatsmeow.MediaImage
	case "video":
		return whatsmeow.MediaVideo
	case "audio":
		return whatsmeow.MediaAudio
	case "document":
		return whatsmeow.MediaDocument
	}
	return ""
}

// optionalString returns a proto string pointer, or nil for an empty value.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return proto.String(value)
}
