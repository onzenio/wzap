// Package sessiontest provides an in-memory session.Manager and session.Session
// for tests. It records calls, lets tests force failures and emits events to a
// session.EventSink.
package sessiontest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
)

var (
	_ session.Manager = (*Fake)(nil)
	_ session.Session = (*FakeSession)(nil)
)

// Fake is an in-memory session.Manager. The forced-error fields make failure
// paths testable and the Calls accessors expose what the code under test did.
type Fake struct {
	mu       sync.Mutex
	sink     session.EventSink
	sessions map[uuid.UUID]*FakeSession

	// RestoreAllErr, CreateErr and RemoveErr, when set, are returned by the
	// matching method.
	RestoreAllErr error
	CreateErr     error
	RemoveErr     error

	restoreCalls int
	createCalls  []*model.Instance
	removeCalls  []uuid.UUID
}

// New returns a fake manager whose sessions emit events to sink (which may be
// nil).
func New(sink session.EventSink) *Fake {
	return &Fake{sink: sink, sessions: make(map[uuid.UUID]*FakeSession)}
}

// RestoreAll records the call and returns the forced error, when set.
func (f *Fake) RestoreAll(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls++
	return f.RestoreAllErr
}

// Get returns the session stored for instanceID.
func (f *Fake) Get(instanceID uuid.UUID) (session.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[instanceID]
	if !ok {
		return nil, false
	}
	return sess, true
}

// Create records the call, returns the forced error when set and otherwise
// stores (or returns) the in-memory session of instance.
func (f *Fake) Create(instance *model.Instance) (session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, instance)
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	if instance == nil {
		return nil, errors.New("create session: nil instance")
	}
	sess, ok := f.sessions[instance.ID]
	if !ok {
		sess = newFakeSession(instance.ID, f.sink)
		f.sessions[instance.ID] = sess
	}
	return sess, nil
}

// Remove records the call, returns the forced error when set and otherwise
// drops the session of instanceID.
func (f *Fake) Remove(_ context.Context, instanceID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, instanceID)
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	delete(f.sessions, instanceID)
	return nil
}

// Put stores sess under instanceID, replacing any previous session. It lets
// tests preinstall sessions before calling the code under test.
func (f *Fake) Put(instanceID uuid.UUID, sess *FakeSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[instanceID] = sess
}

// RestoreCalls returns how many times RestoreAll was called.
func (f *Fake) RestoreCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restoreCalls
}

// CreateCalls returns the instances passed to Create, in order.
func (f *Fake) CreateCalls() []*model.Instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*model.Instance(nil), f.createCalls...)
}

// RemoveCalls returns the instance ids passed to Remove, in order.
func (f *Fake) RemoveCalls() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.removeCalls...)
}

// PresenceCall records one SendPresence invocation.
type PresenceCall struct {
	ChatJID string
	State   string
}

// DeleteCall records one DeleteMessage invocation.
type DeleteCall struct {
	ChatJID   string
	MessageID string
}

// MarkReadCall records one MarkRead invocation.
type MarkReadCall struct {
	ChatJID   string
	SenderJID string
	MessageID string
}

// PairPhoneCall records one PairPhone invocation.
type PairPhoneCall struct {
	Number string
}

// defaultPairPhoneCode is the pairing code a fake returns when the test did
// not configure one.
const defaultPairPhoneCode = "12345678"

// FakeSession is an in-memory session.Session.
type FakeSession struct {
	mu         sync.Mutex
	instanceID uuid.UUID
	sink       session.EventSink

	status      session.Status
	jid         string
	qr          string
	qrExpiresAt time.Time

	// Forced errors, when set, are returned by the matching method.
	ConnectErr       error
	QRErr            error
	SendErr          error
	IsOnWhatsAppErr  error
	SendPresenceErr  error
	DisconnectErr    error
	DeleteMessageErr error
	MarkReadErr      error
	PairPhoneErr     error

	// PairPhoneCode is returned by PairPhone; empty falls back to
	// defaultPairPhoneCode.
	PairPhoneCode string

	// OnWhatsApp maps a phone number to its JID. Numbers absent from the map
	// are reported as not registered.
	OnWhatsApp map[string]string

	connectCalls      int
	disconnectCalls   int
	isOnWhatsAppCalls int
	sends             []session.OutboundMessage
	presences         []PresenceCall
	deletes           []DeleteCall
	markReads         []MarkReadCall
	pairPhones        []PairPhoneCall

	// history accumulates the history-sync feed the Import plan consumes.
	// The zero value is ready to use.
	history session.HistorySyncAccumulator
}

// NewSession returns a standalone fake session for tests that do not go
// through the manager.
func NewSession(instanceID uuid.UUID, sink session.EventSink) *FakeSession {
	return newFakeSession(instanceID, sink)
}

func newFakeSession(instanceID uuid.UUID, sink session.EventSink) *FakeSession {
	return &FakeSession{instanceID: instanceID, sink: sink, status: session.StatusDisconnected}
}

// Connect records the call, moves the session to pairing and returns a stable
// fake QR code valid for a minute.
func (s *FakeSession) Connect(context.Context) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectCalls++
	if s.ConnectErr != nil {
		return "", time.Time{}, s.ConnectErr
	}
	s.status = session.StatusPairing
	if s.qr == "" {
		s.qr = "fake-qr-" + s.instanceID.String()
	}
	if s.qrExpiresAt.IsZero() {
		s.qrExpiresAt = time.Now().Add(time.Minute)
	}
	return s.qr, s.qrExpiresAt, nil
}

// QR returns the QR code produced by the last Connect.
func (s *FakeSession) QR(context.Context) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.QRErr != nil {
		return "", time.Time{}, s.QRErr
	}
	if s.qr == "" {
		return "", time.Time{}, errors.New("no QR code available")
	}
	return s.qr, s.qrExpiresAt, nil
}

// Send records the outbound message and returns a sequential fake id.
func (s *FakeSession) Send(_ context.Context, msg session.OutboundMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, msg)
	if s.SendErr != nil {
		return "", s.SendErr
	}
	return fmt.Sprintf("fake-wamid-%d", len(s.sends)), nil
}

// IsOnWhatsApp answers from OnWhatsApp, treating absent numbers as unknown.
func (s *FakeSession) IsOnWhatsApp(_ context.Context, phone string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isOnWhatsAppCalls++
	if s.IsOnWhatsAppErr != nil {
		return "", false, s.IsOnWhatsAppErr
	}
	jid, ok := s.OnWhatsApp[phone]
	return jid, ok, nil
}

// SendPresence records the presence call.
func (s *FakeSession) SendPresence(_ context.Context, chatJID, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presences = append(s.presences, PresenceCall{ChatJID: chatJID, State: state})
	return s.SendPresenceErr
}

// DeleteMessage records the call and returns the forced error, when set.
func (s *FakeSession) DeleteMessage(_ context.Context, chatJID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, DeleteCall{ChatJID: chatJID, MessageID: messageID})
	return s.DeleteMessageErr
}

// MarkRead records the call and returns the forced error, when set.
func (s *FakeSession) MarkRead(_ context.Context, chatJID, senderJID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markReads = append(s.markReads, MarkReadCall{ChatJID: chatJID, SenderJID: senderJID, MessageID: messageID})
	return s.MarkReadErr
}

// PairPhone records the call, returns the forced error when set and otherwise
// the configured code (or the stable default when none was configured).
func (s *FakeSession) PairPhone(_ context.Context, number string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairPhones = append(s.pairPhones, PairPhoneCall{Number: number})
	if s.PairPhoneErr != nil {
		return "", s.PairPhoneErr
	}
	if s.PairPhoneCode != "" {
		return s.PairPhoneCode, nil
	}
	return defaultPairPhoneCode, nil
}

// HistorySyncSnapshot returns the accumulated history-sync feed, mirroring
// the whatsmeow adapter for the Import plan.
func (s *FakeSession) HistorySyncSnapshot() session.HistorySyncSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history.Snapshot()
}

// ObserveHistorySync folds chunk into the fake feed, like the whatsmeow
// adapter does for live history-sync events.
func (s *FakeSession) ObserveHistorySync(chunk session.HistorySyncChunk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history.Observe(chunk)
}

// Disconnect records the call and moves the session back to disconnected.
func (s *FakeSession) Disconnect(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnectCalls++
	if s.DisconnectErr != nil {
		return s.DisconnectErr
	}
	s.status = session.StatusDisconnected
	s.qr = ""
	s.qrExpiresAt = time.Time{}
	return nil
}

// SetStatus overrides the status so tests can start from a known state.
func (s *FakeSession) SetStatus(status session.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// SetJID overrides the public JID so tests can start from a known state.
func (s *FakeSession) SetJID(jid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jid = jid
}

// Status returns the current status.
func (s *FakeSession) Status() session.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// JID returns the public JID.
func (s *FakeSession) JID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jid
}

// ConnectCalls returns how many times Connect was called.
func (s *FakeSession) ConnectCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectCalls
}

// DisconnectCalls returns how many times Disconnect was called.
func (s *FakeSession) DisconnectCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disconnectCalls
}

// IsOnWhatsAppCalls returns how many times IsOnWhatsApp was called.
func (s *FakeSession) IsOnWhatsAppCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isOnWhatsAppCalls
}

// SendCalls returns the outbound messages passed to Send, in order.
func (s *FakeSession) SendCalls() []session.OutboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.OutboundMessage(nil), s.sends...)
}

// PresenceCalls returns the presence calls, in order.
func (s *FakeSession) PresenceCalls() []PresenceCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PresenceCall(nil), s.presences...)
}

// DeleteCalls returns the delete calls, in order.
func (s *FakeSession) DeleteCalls() []DeleteCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DeleteCall(nil), s.deletes...)
}

// MarkReadCalls returns the read receipt calls, in order.
func (s *FakeSession) MarkReadCalls() []MarkReadCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MarkReadCall(nil), s.markReads...)
}

// PairPhoneCalls returns the pairing code calls, in order.
func (s *FakeSession) PairPhoneCalls() []PairPhoneCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PairPhoneCall(nil), s.pairPhones...)
}

// EmitMessage forwards msg to the sink, when one was configured.
func (s *FakeSession) EmitMessage(msg session.InboundMessage) {
	if s.sink != nil {
		s.sink.OnMessage(context.Background(), msg)
	}
}

// EmitMessageEdit forwards an edit to the sink, when one was configured.
func (s *FakeSession) EmitMessageEdit(edit session.MessageEdit) {
	if s.sink != nil {
		s.sink.OnMessageEdit(context.Background(), edit)
	}
}

// EmitMessageDelete forwards a delete to the sink, when one was configured.
func (s *FakeSession) EmitMessageDelete(del session.MessageDelete) {
	if s.sink != nil {
		s.sink.OnMessageDelete(context.Background(), del)
	}
}

// EmitReceipt forwards receipt to the sink, when one was configured.
func (s *FakeSession) EmitReceipt(receipt session.Receipt) {
	if s.sink != nil {
		s.sink.OnReceipt(context.Background(), receipt)
	}
}

// EmitConnection forwards a connection change for this session to the sink,
// when one was configured.
func (s *FakeSession) EmitConnection(status session.Status, jid, reason string) {
	if s.sink != nil {
		s.sink.OnConnection(context.Background(), s.instanceID, status, jid, reason)
	}
}
