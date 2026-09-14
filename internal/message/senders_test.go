package message

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
)

func TestSendersForwardOutboundMessagePerType(t *testing.T) {
	tests := []struct {
		name    string
		sender  Sender
		msgType string
		payload string
	}{
		{
			name:    "text",
			sender:  textSender{},
			msgType: TypeText,
			payload: `{"text":"olá"}`,
		},
		{
			name:    "quoted text",
			sender:  textSender{},
			msgType: TypeText,
			payload: `{"text":"resposta","quoted_id":"WA-ORIG-1"}`,
		},
		{
			name:    "location",
			sender:  locationSender{},
			msgType: TypeLocation,
			payload: `{"latitude":-23.55,"longitude":-46.63}`,
		},
		{
			name:    "contact",
			sender:  contactSender{},
			msgType: TypeContact,
			payload: `{"display_name":"Fulano","vcard":"BEGIN:VCARD\nVERSION:3.0\nEND:VCARD"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := sessiontest.NewSession(uuid.New(), nil)
			msg := model.OutboundMessage{
				Type:         tt.msgType,
				RecipientJID: "5547988359190@s.whatsapp.net",
				Payload:      []byte(tt.payload),
			}

			whatsappID, err := tt.sender.Send(context.Background(), sess, msg)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if whatsappID != "fake-wamid-1" {
				t.Errorf("whatsapp id = %q, want %q", whatsappID, "fake-wamid-1")
			}

			calls := sess.SendCalls()
			if len(calls) != 1 {
				t.Fatalf("session sends = %d, want 1", len(calls))
			}
			want := session.OutboundMessage{
				Type:         tt.msgType,
				RecipientJID: "5547988359190@s.whatsapp.net",
				Payload:      []byte(tt.payload),
			}
			if !reflect.DeepEqual(calls[0], want) {
				t.Errorf("session message = %+v, want %+v", calls[0], want)
			}
		})
	}
}

func TestSendersRejectInvalidPayloads(t *testing.T) {
	tests := []struct {
		name    string
		sender  Sender
		payload string
	}{
		{name: "text malformed", sender: textSender{}, payload: `{`},
		{name: "text empty", sender: textSender{}, payload: `{"text":"   "}`},
		{name: "location malformed", sender: locationSender{}, payload: `{`},
		{name: "location missing coordinates", sender: locationSender{}, payload: `{}`},
		{name: "location latitude out of range", sender: locationSender{}, payload: `{"latitude":90.1,"longitude":0}`},
		{name: "location longitude out of range", sender: locationSender{}, payload: `{"latitude":0,"longitude":180.1}`},
		{name: "contact malformed", sender: contactSender{}, payload: `{`},
		{name: "contact missing display name", sender: contactSender{}, payload: `{"vcard":"BEGIN:VCARD\nEND:VCARD"}`},
		{name: "contact missing vcard", sender: contactSender{}, payload: `{"display_name":"Fulano"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := sessiontest.NewSession(uuid.New(), nil)

			_, err := tt.sender.Send(context.Background(), sess, model.OutboundMessage{
				Type:         TypeText,
				RecipientJID: "5547988359190@s.whatsapp.net",
				Payload:      []byte(tt.payload),
			})
			if err == nil {
				t.Fatal("Send accepted an invalid payload")
			}
			if len(sess.SendCalls()) != 0 {
				t.Error("session was called with an invalid payload")
			}
		})
	}
}

func TestSendersPropagateSessionErrors(t *testing.T) {
	tests := []struct {
		name   string
		sender Sender
		msg    model.OutboundMessage
	}{
		{
			name:   "text",
			sender: textSender{},
			msg: model.OutboundMessage{
				Type:    TypeText,
				Payload: []byte(`{"text":"olá"}`),
			},
		},
		{
			name:   "location",
			sender: locationSender{},
			msg: model.OutboundMessage{
				Type:    TypeLocation,
				Payload: []byte(`{"latitude":1,"longitude":2}`),
			},
		},
		{
			name:   "contact",
			sender: contactSender{},
			msg: model.OutboundMessage{
				Type:    TypeContact,
				Payload: []byte(`{"display_name":"Fulano","vcard":"BEGIN:VCARD\nEND:VCARD"}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := sessiontest.NewSession(uuid.New(), nil)
			sess.SendErr = fmt.Errorf("%w: down", session.ErrTransient)

			_, err := tt.sender.Send(context.Background(), sess, tt.msg)
			if !errors.Is(err, session.ErrTransient) {
				t.Fatalf("Send error = %v, want a transient session error", err)
			}
		})
	}
}
