package whatsmeow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"wzap/internal/model"
	"wzap/internal/session"
)

// testMediaLimit is the inbound media cap used by the translation tests.
const testMediaLimit = 1 << 20

func TestBuildOutboundMessage(t *testing.T) {
	tests := []struct {
		name    string
		msg     session.OutboundMessage
		check   func(t *testing.T, got *waE2E.Message)
		wantErr bool
	}{
		{
			name: "text",
			msg:  session.OutboundMessage{Type: "text", Payload: []byte(`{"text":"olá"}`)},
			check: func(t *testing.T, got *waE2E.Message) {
				if got.GetConversation() != "olá" {
					t.Fatalf("conversation = %q, want olá", got.GetConversation())
				}
			},
		},
		{
			name: "location",
			msg: session.OutboundMessage{
				Type:    "location",
				Payload: []byte(`{"latitude":-23.55,"longitude":-46.63,"name":"casa","address":"Rua 1"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				loc := got.GetLocationMessage()
				if loc == nil || loc.GetDegreesLatitude() != -23.55 || loc.GetDegreesLongitude() != -46.63 {
					t.Fatalf("location = %v", loc)
				}
				if loc.GetName() != "casa" || loc.GetAddress() != "Rua 1" {
					t.Fatalf("location name/address = %q/%q", loc.GetName(), loc.GetAddress())
				}
			},
		},
		{
			name: "contact",
			msg: session.OutboundMessage{
				Type:    "contact",
				Payload: []byte(`{"display_name":"Fulano","vcard":"BEGIN:VCARD\nVERSION:3.0\nEND:VCARD"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				contact := got.GetContactMessage()
				if contact == nil || contact.GetDisplayName() != "Fulano" {
					t.Fatalf("contact = %v", contact)
				}
				if !strings.HasPrefix(contact.GetVcard(), "BEGIN:VCARD") {
					t.Fatalf("vcard = %q", contact.GetVcard())
				}
			},
		},
		{
			name:    "unsupported type",
			msg:     session.OutboundMessage{Type: "sticker", Payload: []byte(`{}`)},
			wantErr: true,
		},
		{
			name:    "invalid payload",
			msg:     session.OutboundMessage{Type: "text", Payload: []byte(`{`)},
			wantErr: true,
		},
		{
			name:    "empty text",
			msg:     session.OutboundMessage{Type: "text", Payload: []byte(`{"text":""}`)},
			wantErr: true,
		},
		{
			name:    "location missing coordinates",
			msg:     session.OutboundMessage{Type: "location", Payload: []byte(`{}`)},
			wantErr: true,
		},
		{
			name:    "contact missing vcard",
			msg:     session.OutboundMessage{Type: "contact", Payload: []byte(`{"display_name":"Fulano"}`)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildMessage(tt.msg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildMessage(%+v) = %v, want error", tt.msg, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildMessage: %v", err)
			}
			tt.check(t, got)
		})
	}
}

func TestBuildMediaMessage(t *testing.T) {
	upload := whatsmeow.UploadResponse{
		URL:           "https://mmg.whatsapp.net/x",
		DirectPath:    "/v/t62/x",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5, 6},
		FileEncSHA256: []byte{7, 8, 9},
		FileLength:    42,
	}

	tests := []struct {
		name  string
		msg   session.OutboundMessage
		check func(t *testing.T, got *waE2E.Message)
	}{
		{
			name: "image",
			msg: session.OutboundMessage{
				Type:    "image",
				Payload: []byte(`{"caption":"foto","mime_type":"image/jpeg"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				img := got.GetImageMessage()
				if img == nil {
					t.Fatal("image message is nil")
				}
				if img.GetCaption() != "foto" || img.GetMimetype() != "image/jpeg" {
					t.Fatalf("image caption/mime = %q/%q", img.GetCaption(), img.GetMimetype())
				}
				if img.GetURL() != upload.URL || img.GetDirectPath() != upload.DirectPath {
					t.Fatalf("image url/path = %q/%q", img.GetURL(), img.GetDirectPath())
				}
				if img.GetFileLength() != upload.FileLength {
					t.Fatalf("image length = %d, want %d", img.GetFileLength(), upload.FileLength)
				}
			},
		},
		{
			name: "video",
			msg: session.OutboundMessage{
				Type:    "video",
				Payload: []byte(`{"caption":"vídeo","mime_type":"video/mp4"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				video := got.GetVideoMessage()
				if video == nil || video.GetCaption() != "vídeo" || video.GetMimetype() != "video/mp4" {
					t.Fatalf("video = %v", video)
				}
			},
		},
		{
			name: "audio with ptt",
			msg: session.OutboundMessage{
				Type:    "audio",
				Payload: []byte(`{"ptt":true,"mime_type":"audio/ogg; codecs=opus"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				audio := got.GetAudioMessage()
				if audio == nil || !audio.GetPTT() || audio.GetMimetype() != "audio/ogg; codecs=opus" {
					t.Fatalf("audio = %v", audio)
				}
			},
		},
		{
			name: "document",
			msg: session.OutboundMessage{
				Type:    "document",
				Payload: []byte(`{"filename":"nota.pdf","caption":"nota","mime_type":"application/pdf"}`),
			},
			check: func(t *testing.T, got *waE2E.Message) {
				doc := got.GetDocumentMessage()
				if doc == nil || doc.GetFileName() != "nota.pdf" || doc.GetCaption() != "nota" {
					t.Fatalf("document = %v", doc)
				}
				if doc.GetMimetype() != "application/pdf" {
					t.Fatalf("document mime = %q", doc.GetMimetype())
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newMediaMessage(tt.msg, upload)
			if err != nil {
				t.Fatalf("newMediaMessage: %v", err)
			}
			tt.check(t, got)
		})
	}

	if _, err := newMediaMessage(session.OutboundMessage{Type: "sticker"}, upload); err == nil {
		t.Fatal("newMediaMessage accepted an unsupported media type")
	}
	if _, err := newMediaMessage(session.OutboundMessage{Type: "image"}, upload); err == nil {
		t.Fatal("newMediaMessage accepted a media message without a mime type")
	}
}

func TestInboundMessageFromEvent(t *testing.T) {
	instanceID := uuid.New()
	ts := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

	textEvent := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("5511999999999", types.DefaultUserServer),
				Sender: types.NewJID("5511888888888", types.DefaultUserServer),
			},
			ID:        "3EB0ABC",
			Type:      "text",
			Timestamp: ts,
		},
		Message: &waE2E.Message{Conversation: proto.String("bom dia")},
	}

	got := inboundMessage(instanceID, textEvent, nil, testMediaLimit)
	if got.InstanceID != instanceID || got.MessageID != "3EB0ABC" {
		t.Fatalf("identity = %v/%q", got.InstanceID, got.MessageID)
	}
	if got.ChatJID != "5511999999999@s.whatsapp.net" || got.SenderJID != "5511888888888@s.whatsapp.net" {
		t.Fatalf("chat/sender = %q/%q", got.ChatJID, got.SenderJID)
	}
	if got.IsGroup || got.Type != "text" || got.Text != "bom dia" {
		t.Fatalf("body = (group=%v, type=%q, text=%q)", got.IsGroup, got.Type, got.Text)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("timestamp = %v, want %v", got.Timestamp, ts)
	}
	if got.MediaAvailable {
		t.Fatal("text message reported media")
	}
	if got.MediaLength != 0 {
		t.Fatalf("text message announced media length %d, want 0", got.MediaLength)
	}

	groupEvent := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    types.NewJID("123456", types.GroupServer),
				Sender:  types.NewJID("5511888888888", types.DefaultUserServer),
				IsGroup: true,
			},
			ID:   "3EB0DEF",
			Type: "image",
		},
		Message: &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Mimetype:   proto.String("image/jpeg"),
				Caption:    proto.String("olha isso"),
				FileLength: proto.Uint64(4096),
			},
		},
	}

	got = inboundMessage(instanceID, groupEvent, nil, testMediaLimit)
	if !got.IsGroup || !got.MediaAvailable || got.MediaMime != "image/jpeg" {
		t.Fatalf("group media = (group=%v, available=%v, mime=%q)", got.IsGroup, got.MediaAvailable, got.MediaMime)
	}
	if got.MediaLength != 4096 {
		t.Fatalf("group media length = %d, want 4096", got.MediaLength)
	}
	if got.Text != "olha isso" {
		t.Fatalf("media caption = %q, want olha isso", got.Text)
	}
	if got.MediaDownload != nil {
		t.Fatal("media download must be absent without a client")
	}

	documentEvent := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("5511999999999", types.DefaultUserServer)},
			ID:            "3EB0GHI",
			Type:          "document",
		},
		Message: &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				Mimetype: proto.String("application/pdf"),
				FileName: proto.String("contrato.pdf"),
			},
		},
	}

	got = inboundMessage(instanceID, documentEvent, nil, testMediaLimit)
	if !got.MediaAvailable || got.MediaFilename != "contrato.pdf" || got.MediaMime != "application/pdf" {
		t.Fatalf("document = (available=%v, filename=%q, mime=%q)", got.MediaAvailable, got.MediaFilename, got.MediaMime)
	}
}

func TestReceiptFromEvent(t *testing.T) {
	instanceID := uuid.New()
	ts := time.Date(2026, 9, 13, 11, 0, 0, 0, time.UTC)
	evt := &events.Receipt{
		MessageSource: types.MessageSource{
			Chat:   types.NewJID("5511999999999", types.DefaultUserServer),
			Sender: types.NewJID("5511888888888", types.DefaultUserServer),
		},
		MessageIDs: []types.MessageID{"A1", "A2"},
		Timestamp:  ts,
		Type:       types.ReceiptTypeRead,
	}

	got := receiptEvent(instanceID, evt)
	if got.InstanceID != instanceID || got.Status != string(types.ReceiptTypeRead) {
		t.Fatalf("receipt = %+v", got)
	}
	if len(got.MessageIDs) != 2 || got.MessageIDs[0] != "A1" || got.MessageIDs[1] != "A2" {
		t.Fatalf("receipt ids = %v", got.MessageIDs)
	}
	if got.ChatJID != "5511999999999@s.whatsapp.net" || got.SenderJID != "5511888888888@s.whatsapp.net" {
		t.Fatalf("receipt chat/sender = %q/%q", got.ChatJID, got.SenderJID)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("receipt timestamp = %v, want %v", got.Timestamp, ts)
	}
}

func TestConnectionUpdate(t *testing.T) {
	tests := []struct {
		name       string
		evt        any
		wantStatus session.Status
		wantReason string
		wantOK     bool
	}{
		{"connected", &events.Connected{}, session.StatusConnected, "", true},
		{"disconnected", &events.Disconnected{}, session.StatusDisconnected, "", true},
		{"logged out", &events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, session.StatusDisconnected, "401", true},
		{"temporary ban", &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: time.Hour}, session.StatusError, "banned", true},
		{"stream replaced", &events.StreamReplaced{}, session.StatusError, "stream replaced", true},
		{"client outdated", &events.ClientOutdated{}, session.StatusError, "outdated", true},
		{"connect failure", &events.ConnectFailure{Reason: events.ConnectFailureGeneric, Message: "nope"}, session.StatusError, "nope", true},
		{"stream error", &events.StreamError{Code: "500"}, session.StatusError, "500", true},
		{"unrelated", &events.Message{}, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, ok := connectionUpdate(tt.evt)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", status, tt.wantStatus)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
		})
	}
}

func TestParsePresence(t *testing.T) {
	chat, err := parsePresence("composing")
	if err != nil || !chat.isChat || chat.chat != types.ChatPresenceComposing {
		t.Fatalf("composing = (%+v, %v)", chat, err)
	}
	chat, err = parsePresence("paused")
	if err != nil || !chat.isChat || chat.chat != types.ChatPresencePaused {
		t.Fatalf("paused = (%+v, %v)", chat, err)
	}
	user, err := parsePresence("available")
	if err != nil || user.isChat || user.user != types.PresenceAvailable {
		t.Fatalf("available = (%+v, %v)", user, err)
	}
	if _, err := parsePresence("typing"); err == nil {
		t.Fatal("parsePresence accepted an unsupported state")
	}
}

func TestNewSessionRejectsNilDevice(t *testing.T) {
	if _, err := newSession(uuid.New(), nil, nil, nil, testMediaLimit); err == nil {
		t.Fatal("newSession accepted a nil device")
	}
}

func TestDeviceStoreIntegration(t *testing.T) {
	manager := newTestManager(t)
	container := manager.devices

	devices, err := container.GetAllDevices(context.Background())
	if err != nil {
		t.Fatalf("GetAllDevices: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("fresh schema has %d devices, want 0", len(devices))
	}

	jid := saveTestDevice(t, container, "5511999999999")
	stored, err := container.GetDevice(context.Background(), jid)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if stored == nil || stored.PushName != "wzap-test" || stored.ID == nil || stored.ID.String() != jid.String() {
		t.Fatalf("stored device = %+v", stored)
	}
}

func TestRegisterRestoredIsAtomic(t *testing.T) {
	manager := &Manager{sessions: make(map[uuid.UUID]*instanceSession), log: slog.Default()}
	id := uuid.New()
	first := &instanceSession{instanceID: id}
	second := &instanceSession{instanceID: id}

	if !manager.registerRestored(id, first) {
		t.Fatal("first registerRestored returned false, want true")
	}
	if manager.registerRestored(id, second) {
		t.Fatal("second registerRestored returned true, want false")
	}
	got, ok := manager.Get(id)
	if !ok || got != first {
		t.Fatalf("Get = (%v, %v), want the first session", got, ok)
	}
}

func TestAttachAndConnectRegistersOnce(t *testing.T) {
	manager := newTestManager(t)
	var connects int
	manager.restoreConnect = func(context.Context, *instanceSession) error {
		connects++
		return nil
	}

	id := uuid.New()
	device := manager.devices.NewDevice()
	ctx := context.Background()
	if err := manager.attachAndConnect(ctx, id, device); err != nil {
		t.Fatalf("first attachAndConnect: %v", err)
	}
	if err := manager.attachAndConnect(ctx, id, device); err != nil {
		t.Fatalf("second attachAndConnect: %v", err)
	}
	if connects != 1 {
		t.Fatalf("connect calls = %d, want 1", connects)
	}
	if _, ok := manager.Get(id); !ok {
		t.Fatal("registered session not found after attachAndConnect")
	}
}

func TestRemoveDeletesCredentials(t *testing.T) {
	manager := newTestManager(t)
	id := uuid.New()
	jid := saveTestDevice(t, manager.devices, "5511999999999")
	if _, err := manager.Create(&model.Instance{ID: id, WhatsAppJID: jid.String()}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := manager.Remove(context.Background(), id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := manager.Get(id); ok {
		t.Fatal("session still registered after Remove")
	}
	stored, err := manager.devices.GetDevice(context.Background(), jid)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if stored != nil {
		t.Fatal("device credentials still stored after Remove")
	}
}

func TestRemoveLogsOutFromWhatsApp(t *testing.T) {
	manager := newTestManager(t)
	id := uuid.New()
	jid := saveTestDevice(t, manager.devices, "5511999999999")
	sess, err := manager.Create(&model.Instance{ID: id, WhatsAppJID: jid.String()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	logouts := 0
	sess.(*instanceSession).logoutFn = func(context.Context) error {
		logouts++
		return whatsmeow.ErrNotConnected
	}

	if err := manager.Remove(context.Background(), id); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if logouts != 1 {
		t.Errorf("logout attempts = %d, want 1", logouts)
	}
	stored, err := manager.devices.GetDevice(context.Background(), jid)
	if err != nil {
		t.Fatalf("GetDevice after Remove: %v", err)
	}
	if stored != nil {
		t.Error("device credentials still stored after Remove")
	}
}

func TestRemoveKeepsSessionWhenDeleteFails(t *testing.T) {
	manager := newTestManager(t)
	id := uuid.New()
	jid := saveTestDevice(t, manager.devices, "5511888888888")
	if _, err := manager.Create(&model.Instance{ID: id, WhatsAppJID: jid.String()}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := manager.Remove(context.Background(), id); err == nil {
		t.Fatal("Remove with a closed device store returned nil error")
	}
	if _, ok := manager.Get(id); !ok {
		t.Fatal("session was dropped even though the credentials were not deleted")
	}
}

func TestCreateMissingDeviceReturnsErrNoDevice(t *testing.T) {
	manager := newTestManager(t)

	_, err := manager.Create(&model.Instance{ID: uuid.New(), WhatsAppJID: "5511999999999@s.whatsapp.net"})
	if !errors.Is(err, session.ErrNoDevice) {
		t.Fatalf("Create error = %v, want ErrNoDevice", err)
	}
}

func TestConnectDeletedDeviceReturnsErrNoDevice(t *testing.T) {
	sess, err := newSession(uuid.New(), &store.Device{Deleted: true}, nil, nil, testMediaLimit)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	if _, _, err := sess.Connect(context.Background()); !errors.Is(err, session.ErrNoDevice) {
		t.Fatalf("Connect error = %v, want ErrNoDevice", err)
	}
}

// fakeInstanceRepo feeds RestoreAll the persisted instances. Only List is
// expected to be reached.
type fakeInstanceRepo struct {
	instances []model.Instance
}

func (r *fakeInstanceRepo) Create(context.Context, model.Instance) (*model.Instance, error) {
	return nil, errors.New("fakeInstanceRepo.Create: unexpected call")
}

func (r *fakeInstanceRepo) Get(context.Context, uuid.UUID) (*model.Instance, error) {
	return nil, errors.New("fakeInstanceRepo.Get: unexpected call")
}

func (r *fakeInstanceRepo) GetByExternalRef(context.Context, string) (*model.Instance, error) {
	return nil, errors.New("fakeInstanceRepo.GetByExternalRef: unexpected call")
}

func (r *fakeInstanceRepo) List(context.Context, int, string) ([]model.Instance, string, error) {
	return r.instances, "", nil
}

func (r *fakeInstanceRepo) Update(context.Context, model.Instance) (*model.Instance, error) {
	return nil, errors.New("fakeInstanceRepo.Update: unexpected call")
}

func (r *fakeInstanceRepo) SetConnection(context.Context, uuid.UUID, string, string) error {
	return errors.New("fakeInstanceRepo.SetConnection: unexpected call")
}

func (r *fakeInstanceRepo) SetConnectionState(context.Context, uuid.UUID, string, string, string, *time.Time) error {
	return errors.New("fakeInstanceRepo.SetConnectionState: unexpected call")
}

func (r *fakeInstanceRepo) Delete(context.Context, uuid.UUID) error {
	return errors.New("fakeInstanceRepo.Delete: unexpected call")
}

func TestRestoreAllSkipsInstancesWithoutCredentials(t *testing.T) {
	manager := &Manager{
		instances: &fakeInstanceRepo{instances: []model.Instance{{ID: uuid.New(), Status: "disconnected"}}},
		log:       slog.Default(),
		sessions:  make(map[uuid.UUID]*instanceSession),
	}
	restored := false
	manager.restoreConnect = func(context.Context, *instanceSession) error {
		restored = true
		return nil
	}

	if err := manager.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if restored {
		t.Error("RestoreAll restored an instance without a JID")
	}
}

func TestRestoreAllReflectsFailure(t *testing.T) {
	manager := newTestManager(t)
	jid := saveTestDevice(t, manager.devices, "5511999999999")
	sink := &recordingSink{}
	manager.sink = sink
	manager.instances = &fakeInstanceRepo{instances: []model.Instance{{
		ID: uuid.New(), Status: "connected", WhatsAppJID: jid.String(),
	}}}
	manager.restoreConnect = func(context.Context, *instanceSession) error {
		return errors.New("whatsapp unreachable")
	}

	if err := manager.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	event := sink.last(t)
	if event.status != session.StatusError {
		t.Errorf("restore failure event status = %q, want %q", event.status, session.StatusError)
	}
	if !strings.Contains(event.reason, "whatsapp unreachable") {
		t.Errorf("restore failure reason = %q, want the cause", event.reason)
	}
	if event.jid != jid.String() {
		t.Errorf("restore failure JID = %q, want %q", event.jid, jid.String())
	}
}

func TestRestoreAllMissingDeviceReflectsErrNoDevice(t *testing.T) {
	manager := newTestManager(t)
	sink := &recordingSink{}
	manager.sink = sink
	manager.instances = &fakeInstanceRepo{instances: []model.Instance{{
		ID: uuid.New(), Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net",
	}}}

	if err := manager.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	event := sink.last(t)
	if event.status != session.StatusError {
		t.Errorf("missing device event status = %q, want %q", event.status, session.StatusError)
	}
	if !strings.Contains(event.reason, "5511999999999@s.whatsapp.net") {
		t.Errorf("missing device reason = %q, want it to name the device", event.reason)
	}
}

func TestRestoreAllReflectsCancellation(t *testing.T) {
	sink := &recordingSink{}
	instance := model.Instance{ID: uuid.New(), Status: "connected", WhatsAppJID: "5511999999999@s.whatsapp.net"}
	manager := &Manager{
		instances: &fakeInstanceRepo{instances: []model.Instance{instance}},
		log:       slog.Default(),
		sessions:  make(map[uuid.UUID]*instanceSession),
		sink:      sink,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	event := sink.last(t)
	if event.status != session.StatusError {
		t.Errorf("cancelled restore event status = %q, want %q", event.status, session.StatusError)
	}
	if !strings.Contains(event.reason, "restore cancelled") {
		t.Errorf("cancelled restore reason = %q, want it to mention the cancellation", event.reason)
	}
}

// requireTestDSN returns WZAP_TEST_DATABASE_URL, skipping the test when it is
// unset and refusing databases whose name does not end in _test.
func requireTestDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("WZAP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WZAP_TEST_DATABASE_URL to run Postgres integration tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse WZAP_TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatalf("refusing to run against database %q: name must end in _test", cfg.ConnConfig.Database)
	}
	return dsn
}

// createIsolatedSchema creates a uniquely named schema in the test database
// and returns a DSN pointed at it, registering its removal on cleanup.
func createIsolatedSchema(t *testing.T, dsn string) string {
	t.Helper()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	ctx := context.Background()
	schema := fmt.Sprintf("wzap_session_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	return dsnWithSearchPath(t, dsn, schema)
}

// newTestManager returns a Manager backed by a fresh, uniquely named schema of
// the WZAP_TEST_DATABASE_URL database, so integration tests never touch the
// shared public schema. The test is skipped when the variable is unset.
func newTestManager(t *testing.T) *Manager {
	t.Helper()

	manager, err := NewManager(context.Background(), createIsolatedSchema(t, requireTestDSN(t)), nil, slog.Default(), nil, testMediaLimit)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// saveTestDevice stores a minimally initialized paired device and returns its
// JID.
func saveTestDevice(t *testing.T, container *sqlstore.Container, number string) types.JID {
	t.Helper()

	jid := types.NewJID(number, types.DefaultUserServer)
	device := container.NewDevice()
	device.ID = &jid
	device.PushName = "wzap-test"
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := device.Save(context.Background()); err != nil {
		t.Fatalf("save test device: %v", err)
	}
	return jid
}

// dsnWithSearchPath points the sqlstore connections at the isolated test
// schema, keeping the whatsmeow tables out of the shared public schema.
func dsnWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestSessionErrorsAreDistinct(t *testing.T) {
	sentinels := []error{session.ErrTransient, session.ErrNotConnected, session.ErrInvalidRecipient, session.ErrNoDevice}
	for i, err := range sentinels {
		if err == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		for j, other := range sentinels {
			if i != j && errors.Is(err, other) {
				t.Fatalf("sentinel %d matches sentinel %d", i, j)
			}
		}
	}
}
