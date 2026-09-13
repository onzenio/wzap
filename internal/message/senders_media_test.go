package message

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"wzap/internal/media"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/sessiontest"
)

// fakeMediaPaths is an in-memory MediaPathResolver: pathFn configures the
// outcome and calls records the resolved ids.
type fakeMediaPaths struct {
	pathFn func(ctx context.Context, id uuid.UUID) (string, *model.Media, error)
	calls  []uuid.UUID
}

// Path records the id and returns the configured path, defaulting to
// media.ErrNotFound.
func (f *fakeMediaPaths) Path(ctx context.Context, id uuid.UUID) (string, *model.Media, error) {
	f.calls = append(f.calls, id)
	if f.pathFn != nil {
		return f.pathFn(ctx, id)
	}
	return "", nil, media.ErrNotFound
}

// decodePayloadMap decodes a JSON payload into a comparable map.
func decodePayloadMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload %q: %v", raw, err)
	}
	return payload
}

func TestMediaSenderForwardsStoredMedia(t *testing.T) {
	tests := []struct {
		name         string
		mimetype     string
		recordName   string
		payload      string
		wantType     string
		wantCaption  string
		wantFilename string
		wantPTT      bool
	}{
		{
			name:         "image",
			mimetype:     "image/jpeg",
			recordName:   "stored.bin",
			payload:      `{"caption":"olha","filename":"foto.jpg"}`,
			wantType:     media.KindImage,
			wantCaption:  "olha",
			wantFilename: "foto.jpg",
		},
		{
			name:         "video",
			mimetype:     "video/mp4",
			recordName:   "stored.bin",
			payload:      `{"caption":"olha"}`,
			wantType:     media.KindVideo,
			wantCaption:  "olha",
			wantFilename: "stored.bin",
		},
		{
			name:         "audio voice note",
			mimetype:     "audio/ogg",
			recordName:   "voice.ogg",
			payload:      `{"ptt":true}`,
			wantType:     media.KindAudio,
			wantFilename: "voice.ogg",
			wantPTT:      true,
		},
		{
			name:         "document",
			mimetype:     "application/pdf",
			recordName:   "stored.bin",
			payload:      `{"filename":"nota.pdf"}`,
			wantType:     media.KindDocument,
			wantFilename: "nota.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaID := uuid.New()
			msg := model.OutboundMessage{
				ID:           uuid.New(),
				Type:         TypeMedia,
				RecipientJID: "5547988359190@s.whatsapp.net",
				Payload:      []byte(tt.payload),
				MediaID:      &mediaID,
			}
			resolver := &fakeMediaPaths{pathFn: func(_ context.Context, id uuid.UUID) (string, *model.Media, error) {
				if id != mediaID {
					t.Errorf("Path id = %s, want %s", id, mediaID)
				}
				return "/data/media/" + mediaID.String(), &model.Media{
					ID:       mediaID,
					Mimetype: tt.mimetype,
					Filename: tt.recordName,
				}, nil
			}}
			sess := sessiontest.NewSession(uuid.New(), nil)
			sender := mediaSender{media: resolver}

			whatsappID, err := sender.Send(context.Background(), sess, msg)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if whatsappID != "fake-wamid-1" {
				t.Errorf("whatsapp id = %q, want %q", whatsappID, "fake-wamid-1")
			}
			if len(resolver.calls) != 1 {
				t.Fatalf("Path calls = %d, want 1", len(resolver.calls))
			}
			if len(resolver.calls) == 1 && resolver.calls[0] != mediaID {
				t.Errorf("Path id = %s, want %s", resolver.calls[0], mediaID)
			}

			calls := sess.SendCalls()
			if len(calls) != 1 {
				t.Fatalf("session sends = %d, want 1", len(calls))
			}
			out := calls[0]
			if out.Type != tt.wantType {
				t.Errorf("session type = %q, want %q", out.Type, tt.wantType)
			}
			if out.RecipientJID != msg.RecipientJID {
				t.Errorf("session recipient = %q, want %q", out.RecipientJID, msg.RecipientJID)
			}
			if want := "/data/media/" + mediaID.String(); out.MediaPath != want {
				t.Errorf("session MediaPath = %q, want %q", out.MediaPath, want)
			}
			want := map[string]any{"mime_type": tt.mimetype}
			if tt.wantCaption != "" {
				want["caption"] = tt.wantCaption
			}
			if tt.wantFilename != "" {
				want["filename"] = tt.wantFilename
			}
			if tt.wantPTT {
				want["ptt"] = true
			}
			if got := decodePayloadMap(t, out.Payload); !reflect.DeepEqual(got, want) {
				t.Errorf("session payload = %v, want %v", got, want)
			}
		})
	}
}

func TestMediaSenderDefinitiveFailures(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		mediaID  *uuid.UUID
		resolver *fakeMediaPaths
	}{
		{name: "missing media id", payload: `{}`, mediaID: nil, resolver: &fakeMediaPaths{}},
		{name: "malformed payload", payload: `{`, mediaID: uuidPtr(uuid.New()), resolver: validMediaPaths()},
		{
			name:    "unknown media",
			payload: `{"caption":"olha"}`,
			mediaID: uuidPtr(uuid.New()),
			resolver: &fakeMediaPaths{pathFn: func(context.Context, uuid.UUID) (string, *model.Media, error) {
				return "", nil, media.ErrNotFound
			}},
		},
		{
			name:    "expired media",
			payload: `{"caption":"olha"}`,
			mediaID: uuidPtr(uuid.New()),
			resolver: &fakeMediaPaths{pathFn: func(context.Context, uuid.UUID) (string, *model.Media, error) {
				return "", nil, media.ErrExpired
			}},
		},
		{
			name:    "unsupported mimetype",
			payload: `{"caption":"olha"}`,
			mediaID: uuidPtr(uuid.New()),
			resolver: &fakeMediaPaths{pathFn: func(context.Context, uuid.UUID) (string, *model.Media, error) {
				return "/data/media/x", &model.Media{Mimetype: "application/octet-stream"}, nil
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := sessiontest.NewSession(uuid.New(), nil)

			_, err := (mediaSender{media: tt.resolver}).Send(context.Background(), sess, model.OutboundMessage{
				ID:           uuid.New(),
				Type:         TypeMedia,
				RecipientJID: "5547988359190@s.whatsapp.net",
				Payload:      []byte(tt.payload),
				MediaID:      tt.mediaID,
			})
			if err == nil {
				t.Fatal("Send accepted an invalid media message")
			}
			if errors.Is(err, session.ErrTransient) {
				t.Errorf("Send error = %v, want a definitive failure", err)
			}
			if len(sess.SendCalls()) != 0 {
				t.Error("session was called with an invalid media message")
			}
		})
	}
}

func TestMediaSenderWithoutStorageFails(t *testing.T) {
	mediaID := uuid.New()
	sess := sessiontest.NewSession(uuid.New(), nil)

	_, err := (mediaSender{}).Send(context.Background(), sess, model.OutboundMessage{
		Type:         TypeMedia,
		RecipientJID: "5547988359190@s.whatsapp.net",
		Payload:      []byte(`{"caption":"olha"}`),
		MediaID:      &mediaID,
	})
	if err == nil {
		t.Fatal("Send succeeded without a media storage")
	}
	if len(sess.SendCalls()) != 0 {
		t.Error("session was called without a media storage")
	}
}

func TestMediaSenderPropagatesSessionError(t *testing.T) {
	mediaID := uuid.New()
	sess := sessiontest.NewSession(uuid.New(), nil)
	sess.SendErr = errors.New("upload failed")

	_, err := (mediaSender{media: validMediaPaths()}).Send(context.Background(), sess, model.OutboundMessage{
		Type:         TypeMedia,
		RecipientJID: "5547988359190@s.whatsapp.net",
		Payload:      []byte(`{"caption":"olha"}`),
		MediaID:      &mediaID,
	})
	if err == nil {
		t.Fatal("Send did not propagate the session error")
	}
}

func TestDefaultSendersIncludeMedia(t *testing.T) {
	senders := defaultSenders(validMediaPaths())
	if _, ok := senders[TypeMedia]; !ok {
		t.Fatalf("default senders = %v, want a %q sender", senders, TypeMedia)
	}
}

// validMediaPaths returns a resolver that accepts any id as an image.
func validMediaPaths() *fakeMediaPaths {
	return &fakeMediaPaths{pathFn: func(context.Context, uuid.UUID) (string, *model.Media, error) {
		return "/data/media/file", &model.Media{Mimetype: "image/jpeg", Filename: "foto.jpg"}, nil
	}}
}

// uuidPtr returns a pointer to a copy of id.
func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
