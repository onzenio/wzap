package whatsmeow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"

	"wzap/internal/session"
)

// pairingFirstQRTimeout bounds how long Connect waits for the first QR code.
const pairingFirstQRTimeout = 30 * time.Second

// Connect starts the pairing of an instance without credentials, or brings an
// already paired device online returning an empty QR.
//
// The QR channel gets a session-scoped context because callers usually cancel
// the request context as soon as Connect returns, which must not stop the QR
// rotation.
func (s *instanceSession) Connect(ctx context.Context) (string, time.Time, error) {
	s.cancelReconnect()
	if s.client.Store.Deleted {
		return "", time.Time{}, fmt.Errorf("connect session: device deleted: %w", session.ErrNoDevice)
	}
	if s.client.IsConnected() && s.Status() == session.StatusConnected {
		return "", time.Time{}, errors.New("session already connected")
	}
	if s.client.Store.ID != nil {
		if err := s.client.ConnectContext(ctx); err != nil {
			return "", time.Time{}, classifySessionError(err)
		}
		return "", time.Time{}, nil
	}
	if s.client.IsConnected() {
		return "", time.Time{}, errors.New("pairing channel already open")
	}

	qrCtx, cancel := context.WithCancel(context.Background())
	qrChan, err := s.client.GetQRChannel(qrCtx)
	if err != nil {
		cancel()
		return "", time.Time{}, fmt.Errorf("open qr channel: %w", err)
	}

	first := make(chan qrResult, 1)
	s.mu.Lock()
	s.qrCancel = cancel
	s.firstQR = first
	s.mu.Unlock()

	if err := s.client.ConnectContext(ctx); err != nil {
		cancel()
		return "", time.Time{}, classifySessionError(err)
	}
	s.setStatus(session.StatusPairing, "", "")

	go s.monitorQR(qrChan)

	select {
	case res := <-first:
		if res.err != nil {
			return "", time.Time{}, res.err
		}
		return res.code, res.expiresAt, nil
	case <-time.After(pairingFirstQRTimeout):
		return "", time.Time{}, fmt.Errorf("%w: timed out waiting for the first qr code", session.ErrTransient)
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	}
}

// QR returns the current pairing code and its expiry.
func (s *instanceSession) QR(context.Context) (string, time.Time, error) {
	s.mu.RLock()
	status, code, expiresAt := s.status, s.qrCode, s.qrExpiresAt
	s.mu.RUnlock()

	switch {
	case status == session.StatusConnected:
		return "", time.Time{}, errors.New("session already connected")
	case code == "" || time.Now().After(expiresAt):
		return "", time.Time{}, errors.New("no qr code available")
	}
	return code, expiresAt, nil
}

// monitorQR consumes the pairing channel until a final item arrives. The QR
// codes rotate in place: every new code replaces the previous one and a final
// event moves the session status.
func (s *instanceSession) monitorQR(qrChan <-chan whatsmeow.QRChannelItem) {
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			expiresAt := time.Now().Add(item.Timeout)
			s.storeQR(item.Code, expiresAt)
			s.deliverFirstQR(qrResult{code: item.Code, expiresAt: expiresAt})
		case whatsmeow.QRChannelSuccess.Event:
			s.storeQR("", time.Time{})
			s.deliverFirstQR(qrResult{err: errors.New("pairing finished before a qr code was delivered")})
			s.setStatus(session.StatusConnected, s.client.Store.GetJID().String(), "")
			return
		case whatsmeow.QRChannelTimeout.Event:
			s.storeQR("", time.Time{})
			s.deliverFirstQR(qrResult{err: errors.New("qr pairing timed out")})
			s.setStatus(session.StatusDisconnected, "", "qr code expired")
			return
		case whatsmeow.QRChannelEventError:
			s.storeQR("", time.Time{})
			err := item.Error
			if err == nil {
				err = errors.New("qr pairing failed")
			}
			s.deliverFirstQR(qrResult{err: err})
			s.setStatus(session.StatusError, "", err.Error())
			return
		default:
			// Passkey handoff and future intermediate events carry no code.
			s.log.Debug("ignoring qr channel event", "instance_id", s.instanceID, "event", item.Event)
		}
	}
	s.setStatus(session.StatusDisconnected, "", "qr channel closed")
}

// storeQR replaces the current pairing code.
func (s *instanceSession) storeQR(code string, expiresAt time.Time) {
	s.mu.Lock()
	s.qrCode = code
	s.qrExpiresAt = expiresAt
	s.mu.Unlock()
}

// deliverFirstQR hands the pairing outcome to the caller waiting in Connect.
// It is a no-op once the first result was delivered or the caller gave up.
func (s *instanceSession) deliverFirstQR(res qrResult) {
	s.mu.Lock()
	first := s.firstQR
	s.firstQR = nil
	s.mu.Unlock()
	if first != nil {
		first <- res
	}
}
