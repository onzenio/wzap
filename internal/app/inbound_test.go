package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/model"
	"wzap/internal/session"
)

// savedMedia records one inbound Save call.
type savedMedia struct {
	instanceID uuid.UUID
	direction  string
	messageID  string
	mimetype   string
	filename   string
	data       []byte
}

// fakeMediaStore records the inbound media saved by the runtime. The stored row
// carries the id and expiry set by the test so the payload can be checked
// against it.
type fakeMediaStore struct {
	id        uuid.UUID
	expiresAt time.Time
	err       error
	saves     []savedMedia
}

func (f *fakeMediaStore) Save(_ context.Context, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error) {
	f.saves = append(f.saves, savedMedia{
		instanceID: instanceID,
		direction:  direction,
		messageID:  messageID,
		mimetype:   mimetype,
		filename:   filename,
		data:       data,
	})
	if f.err != nil {
		return nil, f.err
	}
	id := f.id
	if id == uuid.Nil {
		id = uuid.New()
	}
	expires := f.expiresAt
	if expires.IsZero() {
		expires = time.Now().UTC().Add(time.Hour)
	}
	return &model.Media{
		ID: id, InstanceID: instanceID, Direction: direction, MessageID: messageID,
		Mimetype: mimetype, Filename: filename, SizeBytes: int64(len(data)),
		CreatedAt: time.Now().UTC(), ExpiresAt: expires,
	}, nil
}

// decodedMessagePayload is the decoded inbound message event body.
type decodedMessagePayload struct {
	FromJID      string           `json:"from_jid"`
	ChatJID      string           `json:"chat_jid"`
	IsGroup      bool             `json:"is_group"`
	MessageID    string           `json:"message_id"`
	Timestamp    time.Time        `json:"timestamp"`
	Type         string           `json:"type"`
	Text         string           `json:"text"`
	Media        *decodedMedia    `json:"media"`
	MediaOmitted *decodedOmission `json:"media_omitted"`
}

// decodedMedia is the media reference of an inbound message event.
type decodedMedia struct {
	MediaID   uuid.UUID `json:"media_id"`
	Mimetype  string    `json:"mimetype"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// decodedOmission explains why the media of an inbound message was not stored.
type decodedOmission struct {
	Reason string `json:"reason"`
}

// decodeMessagePayload unmarshals the payload of a message event, also
// returning the raw keys so tests can assert absent optional fields.
func decodeMessagePayload(t *testing.T, env events.Envelope) (decodedMessagePayload, map[string]json.RawMessage) {
	t.Helper()
	var payload decodedMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode message payload %q: %v", env.Payload, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &raw); err != nil {
		t.Fatalf("decode raw message payload %q: %v", env.Payload, err)
	}
	return payload, raw
}

func TestRuntimeOnMessageTextPublishesMessageEvent(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024, nil)
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	msg := session.InboundMessage{
		InstanceID: id,
		MessageID:  "wamid.text",
		ChatJID:    "5511999999999@s.whatsapp.net",
		SenderJID:  "5511999999999@s.whatsapp.net",
		Type:       "text",
		Text:       "olá",
		Timestamp:  at,
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("handleInbound: %v", err)
	}

	if len(writer.subjects) != 1 {
		t.Fatalf("event writes = %v, want one message event", writer.subjects)
	}
	if want := events.Subjects.Message(id); writer.subjects[0] != want {
		t.Errorf("event subject = %q, want %q", writer.subjects[0], want)
	}
	env := writer.events[0]
	if env.EventVersion != 1 {
		t.Errorf("event_version = %d, want 1", env.EventVersion)
	}
	if env.Type != "message" {
		t.Errorf("event type = %q, want %q", env.Type, "message")
	}
	if env.InstanceID != id {
		t.Errorf("event instance = %s, want %s", env.InstanceID, id)
	}
	if env.EventID == uuid.Nil {
		t.Error("event_id is nil")
	}
	if env.OccurredAt.IsZero() {
		t.Error("occurred_at is zero")
	}

	payload, raw := decodeMessagePayload(t, env)
	if payload.FromJID != msg.SenderJID {
		t.Errorf("payload.from_jid = %q, want %q", payload.FromJID, msg.SenderJID)
	}
	if payload.ChatJID != msg.ChatJID {
		t.Errorf("payload.chat_jid = %q, want %q", payload.ChatJID, msg.ChatJID)
	}
	if payload.IsGroup {
		t.Error("payload.is_group = true, want false")
	}
	if payload.MessageID != msg.MessageID {
		t.Errorf("payload.message_id = %q, want %q", payload.MessageID, msg.MessageID)
	}
	if !payload.Timestamp.Equal(at) {
		t.Errorf("payload.timestamp = %v, want %v", payload.Timestamp, at)
	}
	if payload.Type != "text" {
		t.Errorf("payload.type = %q, want %q", payload.Type, "text")
	}
	if payload.Text != "olá" {
		t.Errorf("payload.text = %q, want %q", payload.Text, "olá")
	}
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none for a text message", payload.Media)
	}
	if payload.MediaOmitted != nil {
		t.Errorf("payload.media_omitted = %+v, want none for a text message", payload.MediaOmitted)
	}
	if _, ok := raw["reply_to"]; ok {
		t.Errorf("payload has reply_to %s, want it dropped from the v1 contract", raw["reply_to"])
	}
}

func TestRuntimeOnMessageGroupIdentifiesParticipantAndChat(t *testing.T) {
	id := uuid.New()
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: id,
		MessageID:  "wamid.group",
		ChatJID:    "120363000000000000@g.us",
		SenderJID:  "5511999999999@s.whatsapp.net",
		IsGroup:    true,
		Type:       "text",
		Text:       "bom dia",
		Timestamp:  time.Now().UTC(),
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("handleInbound: %v", err)
	}

	payload, _ := decodeMessagePayload(t, writer.events[0])
	if !payload.IsGroup {
		t.Error("payload.is_group = false, want true")
	}
	if payload.FromJID != msg.SenderJID {
		t.Errorf("payload.from_jid = %q, want the participant %q", payload.FromJID, msg.SenderJID)
	}
	if payload.ChatJID != msg.ChatJID {
		t.Errorf("payload.chat_jid = %q, want the group %q", payload.ChatJID, msg.ChatJID)
	}
}

func TestRuntimeOnMessageMediaWithinLimitIsSavedAndReferenced(t *testing.T) {
	id := uuid.New()
	mediaID := uuid.New()
	expires := time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC)
	store := &fakeMediaStore{id: mediaID, expiresAt: expires}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 8, nil)
	data := []byte("12345678")
	msg := session.InboundMessage{
		InstanceID:     id,
		MessageID:      "wamid.media",
		ChatJID:        "5511999999999@s.whatsapp.net",
		SenderJID:      "5511999999999@s.whatsapp.net",
		Type:           "image",
		Timestamp:      time.Now().UTC(),
		MediaAvailable: true,
		MediaMime:      "image/jpeg",
		MediaFilename:  "foto.jpg",
		MediaDownload:  func(context.Context) ([]byte, error) { return data, nil },
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("handleInbound: %v", err)
	}

	if len(store.saves) != 1 {
		t.Fatalf("media saves = %d, want 1", len(store.saves))
	}
	saved := store.saves[0]
	if saved.instanceID != id {
		t.Errorf("saved instance = %s, want %s", saved.instanceID, id)
	}
	if saved.direction != "inbound" {
		t.Errorf("saved direction = %q, want %q", saved.direction, "inbound")
	}
	if saved.messageID != msg.MessageID {
		t.Errorf("saved message = %q, want %q", saved.messageID, msg.MessageID)
	}
	if saved.mimetype != "image/jpeg" || saved.filename != "foto.jpg" {
		t.Errorf("saved metadata = %q/%q, want image/jpeg/foto.jpg", saved.mimetype, saved.filename)
	}
	if !bytes.Equal(saved.data, data) {
		t.Errorf("saved content = %q, want %q", saved.data, data)
	}

	payload, raw := decodeMessagePayload(t, writer.events[0])
	if payload.Media == nil {
		t.Fatalf("payload.media = nil, want the stored media: %s", writer.events[0].Payload)
	}
	if payload.Media.MediaID != mediaID {
		t.Errorf("payload.media.media_id = %s, want %s", payload.Media.MediaID, mediaID)
	}
	if payload.Media.Mimetype != "image/jpeg" {
		t.Errorf("payload.media.mimetype = %q, want %q", payload.Media.Mimetype, "image/jpeg")
	}
	if payload.Media.Filename != "foto.jpg" {
		t.Errorf("payload.media.filename = %q, want %q", payload.Media.Filename, "foto.jpg")
	}
	if payload.Media.Size != int64(len(data)) {
		t.Errorf("payload.media.size = %d, want %d", payload.Media.Size, len(data))
	}
	if want := "https://wzap.example.com/api/v1/media/" + mediaID.String(); payload.Media.URL != want {
		t.Errorf("payload.media.url = %q, want %q", payload.Media.URL, want)
	}
	if !payload.Media.ExpiresAt.Equal(expires) {
		t.Errorf("payload.media.expires_at = %v, want %v", payload.Media.ExpiresAt, expires)
	}
	if payload.MediaOmitted != nil {
		t.Errorf("payload.media_omitted = %+v, want none", payload.MediaOmitted)
	}
	if _, ok := raw["text"]; ok {
		t.Errorf("payload has text %s, want it omitted for a media message without caption", raw["text"])
	}
}

func TestRuntimeOnMessageMediaURLTrimsTrailingSlash(t *testing.T) {
	mediaID := uuid.New()
	store := &fakeMediaStore{id: mediaID}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com/", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.media", Type: "image",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "image/jpeg",
		MediaDownload: func(context.Context) ([]byte, error) { return []byte("x"), nil },
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("handleInbound: %v", err)
	}

	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media == nil {
		t.Fatal("payload.media = nil, want the stored media")
	}
	if want := "https://wzap.example.com/api/v1/media/" + mediaID.String(); payload.Media.URL != want {
		t.Errorf("payload.media.url = %q, want %q", payload.Media.URL, want)
	}
}

func TestRuntimeOnMessageMediaAboveLimitIsOmitted(t *testing.T) {
	store := &fakeMediaStore{}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 8, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.big", Type: "video",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "video/mp4",
		MediaDownload: func(context.Context) ([]byte, error) { return []byte("123456789"), nil },
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("over-limit media must not fail the flow: %v", err)
	}

	if len(store.saves) != 0 {
		t.Errorf("media saves = %+v, want none for over-limit media", store.saves)
	}
	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none for over-limit media", payload.Media)
	}
	if payload.MediaOmitted == nil {
		t.Fatalf("payload.media_omitted = nil, want a reason: %s", writer.events[0].Payload)
	}
	if payload.MediaOmitted.Reason != "media exceeds the size limit" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "media exceeds the size limit")
	}
}

func TestRuntimeOnMessageMediaLengthAboveLimitIsOmitted(t *testing.T) {
	store := &fakeMediaStore{}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 8, nil)
	downloads := 0
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.knownbig", Type: "video",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "video/mp4",
		MediaLength: 9,
		MediaDownload: func(context.Context) ([]byte, error) {
			downloads++
			return []byte("12345678"), nil
		},
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("over-limit media must not fail the flow: %v", err)
	}

	if downloads != 0 {
		t.Errorf("download calls = %d, want none when MediaLength is above the limit", downloads)
	}
	if len(store.saves) != 0 {
		t.Errorf("media saves = %+v, want none for over-limit media", store.saves)
	}
	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none for over-limit media", payload.Media)
	}
	if payload.MediaOmitted == nil {
		t.Fatalf("payload.media_omitted = nil, want a reason: %s", writer.events[0].Payload)
	}
	if payload.MediaOmitted.Reason != "media exceeds the size limit" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "media exceeds the size limit")
	}
}

func TestRuntimeOnMessageMediaStreamOverCapIsOmitted(t *testing.T) {
	store := &fakeMediaStore{}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 8, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.stream", Type: "video",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "video/mp4",
		MediaDownload: func(context.Context) ([]byte, error) {
			return nil, errors.New("media download exceeds the size limit")
		},
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("an over-cap stream must not fail the flow: %v", err)
	}

	if len(store.saves) != 0 {
		t.Errorf("media saves = %+v, want none for an over-cap stream", store.saves)
	}
	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none for an over-cap stream", payload.Media)
	}
	if payload.MediaOmitted == nil {
		t.Fatalf("payload.media_omitted = nil, want the cap failure: %s", writer.events[0].Payload)
	}
	if payload.MediaOmitted.Reason != "download failed: media download exceeds the size limit" {
		t.Errorf("payload.media_omitted.reason = %q, want the download cap failure", payload.MediaOmitted.Reason)
	}
}

func TestRuntimeOnMessageMediaDownloadFailureIsOmitted(t *testing.T) {
	store := &fakeMediaStore{}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.broken", Type: "image",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "image/jpeg",
		MediaDownload: func(context.Context) ([]byte, error) { return nil, errors.New("network down") },
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("download failure must not fail the flow: %v", err)
	}

	if len(store.saves) != 0 {
		t.Errorf("media saves = %+v, want none when the download failed", store.saves)
	}
	if len(writer.events) != 1 {
		t.Fatalf("event writes = %d, want the message event anyway", len(writer.events))
	}
	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none when the download failed", payload.Media)
	}
	if payload.MediaOmitted == nil {
		t.Fatal("payload.media_omitted = nil, want the failure reason")
	}
	if payload.MediaOmitted.Reason != "download failed: network down" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "download failed: network down")
	}
}

func TestRuntimeOnMessageMediaDownloadUnavailableIsOmitted(t *testing.T) {
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, &fakeMediaStore{}, "https://wzap.example.com", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.nodl", Type: "image",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "image/jpeg",
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("missing downloader must not fail the flow: %v", err)
	}

	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.MediaOmitted == nil {
		t.Fatal("payload.media_omitted = nil, want the missing downloader reason")
	}
	if payload.MediaOmitted.Reason != "media download unavailable" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "media download unavailable")
	}
}

func TestRuntimeOnMessageMediaStoreFailureIsOmittedAndReported(t *testing.T) {
	store := &fakeMediaStore{err: errors.New("database down")}
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, store, "https://wzap.example.com", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.dbfail", Type: "image",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "image/jpeg",
		MediaDownload: func(context.Context) ([]byte, error) { return []byte("x"), nil },
	}

	err := runtime.handleInbound(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "save inbound media") {
		t.Fatalf("handleInbound error = %v, want the save failure", err)
	}

	if len(writer.events) != 1 {
		t.Fatalf("event writes = %d, want the message event anyway", len(writer.events))
	}
	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Media != nil {
		t.Errorf("payload.media = %+v, want none when the store failed", payload.Media)
	}
	if payload.MediaOmitted == nil {
		t.Fatal("payload.media_omitted = nil, want the store failure reason")
	}
	if payload.MediaOmitted.Reason != "media store failed" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "media store failed")
	}
}

func TestRuntimeOnMessageMediaStoreUnavailableIsOmitted(t *testing.T) {
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024, nil)
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.nostore", Type: "image",
		Timestamp: time.Now().UTC(), MediaAvailable: true, MediaMime: "image/jpeg",
		MediaDownload: func(context.Context) ([]byte, error) { return []byte("x"), nil },
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("missing media store must not fail the flow: %v", err)
	}

	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.MediaOmitted == nil {
		t.Fatal("payload.media_omitted = nil, want the missing storage reason")
	}
	if payload.MediaOmitted.Reason != "media storage unavailable" {
		t.Errorf("payload.media_omitted.reason = %q, want %q", payload.MediaOmitted.Reason, "media storage unavailable")
	}
}

func TestRuntimeOnMessageZeroTimestampFallsBackToNow(t *testing.T) {
	writer := &fakeWriter{}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024, nil)
	before := time.Now().UTC()
	msg := session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.notime", Type: "text", Text: "oi",
	}

	if err := runtime.handleInbound(context.Background(), msg); err != nil {
		t.Fatalf("handleInbound: %v", err)
	}
	after := time.Now().UTC()

	payload, _ := decodeMessagePayload(t, writer.events[0])
	if payload.Timestamp.IsZero() {
		t.Fatal("payload.timestamp is zero, want the reception time")
	}
	if payload.Timestamp.Before(before) || payload.Timestamp.After(after) {
		t.Errorf("payload.timestamp = %v, want between %v and %v", payload.Timestamp, before, after)
	}
}

func TestRuntimeOnMessageEventFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	writer := &fakeWriter{writeErr: errors.New("outbox down")}
	runtime := NewRuntime(newRuntimeRepo(), writer, nil, nil, "https://wzap.example.com", 1024,
		slog.New(slog.NewTextHandler(&logs, nil)))

	runtime.OnMessage(context.Background(), session.InboundMessage{
		InstanceID: uuid.New(), MessageID: "wamid.fail", Type: "text",
		Text: "oi", Timestamp: time.Now().UTC(),
	})

	if !strings.Contains(logs.String(), "handle inbound message") || !strings.Contains(logs.String(), "outbox down") {
		t.Errorf("logs = %q, want the inbound event failure", logs.String())
	}
}
