package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"wzap/internal/media"
	"wzap/internal/model"
	"wzap/internal/session"
)

// MediaPathResolver resolves a stored media to the file the session reads on
// upload, with the same existence and expiry checks as the download path.
// *media.Storage implements it.
type MediaPathResolver interface {
	Path(ctx context.Context, id uuid.UUID) (string, *model.Media, error)
}

// The concrete storage satisfies the resolver contract; the assertion catches
// signature drift at build time.
var _ MediaPathResolver = (*media.Storage)(nil)

// mediaOutboundPayload is the session body of a media message: the stored
// caption and filename plus the mimetype resolved from the media row.
// QuotedID is the WhatsApp id being replied to, omitted when not a quote.
type mediaOutboundPayload struct {
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mime_type"`
	PTT      bool   `json:"ptt,omitempty"`
	QuotedID string `json:"quoted_id,omitempty"`
}

// mediaSender loads the stored media of a message, derives the WhatsApp media
// kind from its mimetype and hands the session the file to upload. A missing
// or expired file is a definitive failure: retrying cannot bring it back.
type mediaSender struct {
	media MediaPathResolver
}

// Send resolves the media of msg and forwards it to the session.
func (s mediaSender) Send(ctx context.Context, sess session.Session, msg model.OutboundMessage) (string, error) {
	if s.media == nil {
		return "", errors.New("media payload: media storage is not configured")
	}
	if msg.MediaID == nil {
		return "", errors.New("media payload: media_id is required")
	}

	path, record, err := s.media.Path(ctx, *msg.MediaID)
	if err != nil {
		return "", fmt.Errorf("media message %s: %w", msg.ID, err)
	}

	var payload mediaPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return "", fmt.Errorf("media payload: %w", err)
	}

	kind, ok := media.Kind(record.Mimetype)
	if !ok {
		return "", fmt.Errorf("media payload: unsupported mimetype %q", record.Mimetype)
	}

	filename := payload.Filename
	if filename == "" {
		filename = record.Filename
	}
	body, err := json.Marshal(mediaOutboundPayload{
		Caption:  payload.Caption,
		Filename: filename,
		MimeType: record.Mimetype,
		PTT:      payload.PTT,
		QuotedID: payload.QuotedID,
	})
	if err != nil {
		return "", fmt.Errorf("media payload: %w", err)
	}

	return sess.Send(ctx, session.OutboundMessage{
		Type:         kind,
		RecipientJID: msg.RecipientJID,
		Payload:      body,
		MediaPath:    path,
	})
}
