package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/media"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/webhook"
)

// messageEventType is the event type of the inbound message events.
const messageEventType = "message"

// mediaDirectionInbound labels media downloaded from a received message.
const mediaDirectionInbound = "inbound"

// MediaStore persists the media of an inbound message. *media.Storage
// implements it.
type MediaStore interface {
	Save(
		ctx context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte,
	) (*model.Media, error)
}

// The concrete storage satisfies the inbound media contract; the assertion
// catches signature drift at build time.
var _ MediaStore = (*media.Storage)(nil)

// handleInbound stores the media of msg when it has one and enqueues its
// message event. Media that cannot be stored is reported as media_omitted in
// the payload instead of failing the flow; the returned error is reserved for
// a failed persist after a successful download or a failed outbox write, so
// the sink can log it while the event still reaches the consumers.
func (r *Runtime) handleInbound(ctx context.Context, msg session.InboundMessage) error {
	payload := messagePayload{
		FromJID:   msg.SenderJID,
		ChatJID:   msg.ChatJID,
		IsGroup:   msg.IsGroup,
		MessageID: msg.MessageID,
		Timestamp: msg.Timestamp,
		Type:      msg.Type,
		Text:      msg.Text,
	}
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}

	var mediaErr error
	if msg.MediaAvailable {
		mediaErr = r.attachMedia(ctx, &payload, msg)
	}

	env, err := events.New(messageEventType, msg.InstanceID, payload)
	if err != nil {
		return fmt.Errorf("build message event: %w", err)
	}
	// The trimmed raw rides the envelope for NATS and webhook alike, so both
	// consumers share the same bounded event. Connection and message.status
	// envelopes carry no event: there is no meaningful raw upstream payload.
	if trimmed, _ := webhook.CutRawForLimit(msg.Raw, r.maxMediaBytes); len(trimmed) > 0 {
		env.Event = trimmed
	}
	if err := r.events.Write(ctx, events.Subjects.Message(msg.InstanceID), env); err != nil {
		return fmt.Errorf("enqueue message event: %w", err)
	}
	return mediaErr
}

// attachMedia stores the media of msg and fills payload.Media, falling back to
// payload.MediaOmitted with a reason when the content cannot be stored. The
// error is returned only when the storage itself failed, after the download.
func (r *Runtime) attachMedia(ctx context.Context, payload *messagePayload, msg session.InboundMessage) error {
	if msg.MediaLength > r.maxMediaBytes {
		payload.MediaOmitted = &mediaOmission{Reason: "media exceeds the size limit"}
		return nil
	}
	if msg.MediaDownload == nil {
		payload.MediaOmitted = &mediaOmission{Reason: "media download unavailable"}
		return nil
	}
	if r.media == nil {
		payload.MediaOmitted = &mediaOmission{Reason: "media storage unavailable"}
		return nil
	}

	data, err := msg.MediaDownload(ctx)
	if err != nil {
		payload.MediaOmitted = &mediaOmission{Reason: "download failed: " + err.Error()}
		return nil
	}
	if int64(len(data)) > r.maxMediaBytes {
		payload.MediaOmitted = &mediaOmission{Reason: "media exceeds the size limit"}
		return nil
	}

	stored, err := r.media.Save(
		ctx, msg.InstanceID, mediaDirectionInbound, msg.MessageID, msg.MediaMime, msg.MediaFilename, data,
	)
	if err != nil {
		payload.MediaOmitted = &mediaOmission{Reason: "media store failed"}
		return fmt.Errorf("save inbound media: %w", err)
	}

	payload.Media = &mediaPayload{
		MediaID:   stored.ID,
		Mimetype:  stored.Mimetype,
		Filename:  stored.Filename,
		Size:      stored.SizeBytes,
		URL:       r.mediaURL(stored.ID),
		ExpiresAt: stored.ExpiresAt,
	}
	return nil
}

// mediaURL builds the authenticated download URL of stored media.
func (r *Runtime) mediaURL(id uuid.UUID) string {
	return strings.TrimSuffix(r.publicURL, "/") + "/media/" + id.String()
}

// messagePayload is the JSON body of an inbound message event. Reply/quoting
// is out of scope for v1 and stays absent until a future change adds it.
type messagePayload struct {
	FromJID      string         `json:"from_jid"`
	ChatJID      string         `json:"chat_jid"`
	IsGroup      bool           `json:"is_group"`
	MessageID    string         `json:"message_id"`
	Timestamp    time.Time      `json:"timestamp"`
	Type         string         `json:"type"`
	Text         string         `json:"text,omitempty"`
	Media        *mediaPayload  `json:"media,omitempty"`
	MediaOmitted *mediaOmission `json:"media_omitted,omitempty"`
}

// mediaPayload references stored media: the opaque id, the metadata and the
// authenticated URL the consumer fetches it from before it expires.
type mediaPayload struct {
	MediaID   uuid.UUID `json:"media_id"`
	Mimetype  string    `json:"mimetype"`
	Filename  string    `json:"filename,omitempty"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mediaOmission explains why the media of a message was not stored.
type mediaOmission struct {
	Reason string `json:"reason"`
}
