package app

import (
	"context"
	"time"

	"wzap/internal/events"
	"wzap/internal/session"
	"wzap/internal/webhook"
)

// messageEditEventType is the event type of the inbound message edit events.
const messageEditEventType = "message.edit"

// messageDeleteEventType is the event type of the inbound message delete
// events.
const messageDeleteEventType = "message.delete"

// OnMessageEdit enqueues the edit event of a previously received message.
// Failures are logged: the sink must not bring the session down.
func (r *Runtime) OnMessageEdit(ctx context.Context, edit session.MessageEdit) {
	payload := messageEditPayload{
		FromJID:   edit.SenderJID,
		ChatJID:   edit.ChatJID,
		IsGroup:   edit.IsGroup,
		MessageID: edit.MessageID,
		Text:      edit.Text,
		Timestamp: edit.Timestamp,
	}
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}
	env, err := events.New(messageEditEventType, edit.InstanceID, payload)
	if err != nil {
		r.log.ErrorContext(ctx, "build message edit event",
			"instance_id", edit.InstanceID, "message_id", edit.MessageID, "error", err)
		return
	}
	// The trimmed raw rides the envelope for NATS and webhook alike, like the
	// inbound message events do.
	if trimmed, _ := webhook.CutRawForLimit(edit.Raw, r.maxMediaBytes); len(trimmed) > 0 {
		env.Event = trimmed
	}
	if err := r.events.Write(ctx, events.Subjects.MessageEdit(edit.InstanceID), env); err != nil {
		r.log.ErrorContext(ctx, "enqueue message edit event",
			"instance_id", edit.InstanceID, "message_id", edit.MessageID, "error", err)
	}
}

// OnMessageDelete enqueues the delete event of a previously received message.
// Failures are logged: the sink must not bring the session down.
func (r *Runtime) OnMessageDelete(ctx context.Context, del session.MessageDelete) {
	payload := messageDeletePayload{
		FromJID:   del.SenderJID,
		ChatJID:   del.ChatJID,
		IsGroup:   del.IsGroup,
		MessageID: del.MessageID,
		Timestamp: del.Timestamp,
	}
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}
	env, err := events.New(messageDeleteEventType, del.InstanceID, payload)
	if err != nil {
		r.log.ErrorContext(ctx, "build message delete event",
			"instance_id", del.InstanceID, "message_id", del.MessageID, "error", err)
		return
	}
	if trimmed, _ := webhook.CutRawForLimit(del.Raw, r.maxMediaBytes); len(trimmed) > 0 {
		env.Event = trimmed
	}
	if err := r.events.Write(ctx, events.Subjects.MessageDelete(del.InstanceID), env); err != nil {
		r.log.ErrorContext(ctx, "enqueue message delete event",
			"instance_id", del.InstanceID, "message_id", del.MessageID, "error", err)
	}
}

// messageEditPayload is the JSON body of an inbound message edit event: the
// original message id, the replacement content and the edit moment. The key
// names mirror the inbound message events so consumers reuse the correlation.
type messageEditPayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Text      string    `json:"text,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// messageDeletePayload is the JSON body of an inbound message delete event:
// the original message id and the moment the revocation was observed.
type messageDeletePayload struct {
	FromJID   string    `json:"from_jid"`
	ChatJID   string    `json:"chat_jid"`
	IsGroup   bool      `json:"is_group"`
	MessageID string    `json:"message_id"`
	Timestamp time.Time `json:"timestamp"`
}
