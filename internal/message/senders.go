package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"wzap/internal/model"
	"wzap/internal/session"
)

// Sender delivers one stored outbound message through a session. The outbox
// dispatches each claimed message to the sender of its type; the sender
// validates the stored payload and translates it into the session message.
type Sender interface {
	Send(ctx context.Context, sess session.Session, msg model.OutboundMessage) (string, error)
}

// The per-type senders satisfy the contract; the assertions catch signature
// drift at build time.
var (
	_ Sender = textSender{}
	_ Sender = locationSender{}
	_ Sender = contactSender{}
	_ Sender = mediaSender{}
)

// defaultSenders maps every message type accepted by Enqueue to its sender.
// A nil media resolver leaves media messages unsupported.
func defaultSenders(media MediaPathResolver) map[string]Sender {
	return map[string]Sender{
		TypeText:     textSender{},
		TypeLocation: locationSender{},
		TypeContact:  contactSender{},
		TypeMedia:    mediaSender{media: media},
	}
}

// textSender validates the stored text body before handing it to the session.
type textSender struct{}

// Send rejects a malformed or empty text payload and forwards the message.
func (textSender) Send(ctx context.Context, sess session.Session, msg model.OutboundMessage) (string, error) {
	var payload textPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return "", fmt.Errorf("text payload: %w", err)
	}
	if strings.TrimSpace(payload.Text) == "" {
		return "", errors.New("text payload: text is required")
	}
	return sess.Send(ctx, session.OutboundMessage{
		Type:         msg.Type,
		RecipientJID: msg.RecipientJID,
		Payload:      msg.Payload,
	})
}

// locationSender validates the stored coordinates before handing the message to
// the session. The pointers distinguish an absent coordinate from a zero one.
type locationSender struct{}

// Send rejects a location payload without valid coordinates and forwards the
// message.
func (locationSender) Send(ctx context.Context, sess session.Session, msg model.OutboundMessage) (string, error) {
	var payload struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return "", fmt.Errorf("location payload: %w", err)
	}
	if payload.Latitude == nil || payload.Longitude == nil {
		return "", errors.New("location payload: latitude and longitude are required")
	}
	if *payload.Latitude < -90 || *payload.Latitude > 90 {
		return "", errors.New("location payload: latitude out of range")
	}
	if *payload.Longitude < -180 || *payload.Longitude > 180 {
		return "", errors.New("location payload: longitude out of range")
	}
	return sess.Send(ctx, session.OutboundMessage{
		Type:         msg.Type,
		RecipientJID: msg.RecipientJID,
		Payload:      msg.Payload,
	})
}

// contactSender validates the stored vCard before handing the message to the
// session.
type contactSender struct{}

// Send rejects a contact payload without a display name or vCard and forwards
// the message.
func (contactSender) Send(ctx context.Context, sess session.Session, msg model.OutboundMessage) (string, error) {
	var payload contactPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return "", fmt.Errorf("contact payload: %w", err)
	}
	if strings.TrimSpace(payload.DisplayName) == "" {
		return "", errors.New("contact payload: display_name is required")
	}
	if strings.TrimSpace(payload.VCard) == "" {
		return "", errors.New("contact payload: vcard is required")
	}
	return sess.Send(ctx, session.OutboundMessage{
		Type:         msg.Type,
		RecipientJID: msg.RecipientJID,
		Payload:      msg.Payload,
	})
}
