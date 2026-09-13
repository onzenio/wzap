package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"wzap/internal/media"
	"wzap/internal/message"
	"wzap/internal/model"
)

const (
	// defaultMessagesLimit is the page size used when the request omits limit.
	defaultMessagesLimit = 50
	// maxMessagesLimit caps the page size a client can request.
	maxMessagesLimit = 100
	// mediaDirectionOutbound labels media uploaded for a send.
	mediaDirectionOutbound = "outbound"
	// mediaFormMemory is how much of an upload ParseMultipartForm keeps in
	// memory; larger file parts spill to temporary files.
	mediaFormMemory = 1 << 20
	// mediaFormOverhead is the slack above the configured media limit that
	// covers the multipart boundaries, part headers and text fields.
	mediaFormOverhead = 1 << 20
)

// MessageService is the message acceptance and query contract consumed by the
// handlers.
type MessageService interface {
	Enqueue(ctx context.Context, instanceID uuid.UUID, input message.EnqueueInput) (uuid.UUID, error)
	Get(ctx context.Context, instanceID, messageID uuid.UUID) (*model.OutboundMessage, error)
	List(ctx context.Context, instanceID uuid.UUID, limit int, cursor string) ([]model.OutboundMessage, string, error)
}

// The service satisfies the handler contract; the assertion catches signature
// drift at build time.
var _ MessageService = (*message.Service)(nil)

// messageAcceptedResponse is the 202 answer to an accepted send.
type messageAcceptedResponse struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

// messageResponse is the JSON representation of a queued or delivered message.
type messageResponse struct {
	ID                string     `json:"id"`
	InstanceID        string     `json:"instance_id"`
	Type              string     `json:"type"`
	Recipient         string     `json:"recipient"`
	Status            string     `json:"status"`
	WhatsAppMessageID string     `json:"whatsapp_message_id"`
	LastError         string     `json:"last_error"`
	Attempts          int        `json:"attempts"`
	DeliveredAt       *time.Time `json:"delivered_at"`
	ReadAt            *time.Time `json:"read_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// messageListResponse is the JSON representation of a message page.
type messageListResponse struct {
	Items      []messageResponse `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// sendTextRequest is the POST /instances/{id}/messages/text payload.
type sendTextRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

// sendLocationRequest is the POST /instances/{id}/messages/location payload.
// The coordinates are pointers so a missing field is distinct from zero.
type sendLocationRequest struct {
	To        string   `json:"to"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// sendContactRequest is the POST /instances/{id}/messages/contact payload.
type sendContactRequest struct {
	To          string `json:"to"`
	DisplayName string `json:"display_name"`
	VCard       string `json:"vcard"`
}

// handleSendText accepts a text message and answers 202 with its id.
func handleSendText(messages MessageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		var request sendTextRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		messageID, err := messages.Enqueue(r.Context(), id, message.EnqueueInput{
			Type: message.TypeText,
			To:   request.To,
			Text: request.Text,
		})
		if err != nil {
			writeMessageError(w, r, err)
			return
		}
		JSON(w, http.StatusAccepted, newMessageAcceptedResponse(messageID))
	}
}

// handleSendLocation accepts a location message and answers 202 with its id.
func handleSendLocation(messages MessageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		var request sendLocationRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}
		if request.Latitude == nil || request.Longitude == nil {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "latitude and longitude are required")
			return
		}

		messageID, err := messages.Enqueue(r.Context(), id, message.EnqueueInput{
			Type:      message.TypeLocation,
			To:        request.To,
			Latitude:  *request.Latitude,
			Longitude: *request.Longitude,
		})
		if err != nil {
			writeMessageError(w, r, err)
			return
		}
		JSON(w, http.StatusAccepted, newMessageAcceptedResponse(messageID))
	}
}

// handleSendContact accepts a contact message and answers 202 with its id.
func handleSendContact(messages MessageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		var request sendContactRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			writeJSONBodyError(w, r, err)
			return
		}

		messageID, err := messages.Enqueue(r.Context(), id, message.EnqueueInput{
			Type:        message.TypeContact,
			To:          request.To,
			DisplayName: request.DisplayName,
			VCard:       request.VCard,
		})
		if err != nil {
			writeMessageError(w, r, err)
			return
		}
		JSON(w, http.StatusAccepted, newMessageAcceptedResponse(messageID))
	}
}

// handleSendMedia accepts a multipart media upload, stores the file and
// enqueues a media message referencing it. The declared type must be one of
// the outbound kinds and must match the uploaded content type; invalid content
// answers 422 before the media is stored or the message enqueued.
func handleSendMedia(messages MessageService, mediaStore MediaStore, maxBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		if maxBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes+mediaFormOverhead)
		}
		if err := r.ParseMultipartForm(mediaFormMemory); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file exceeds the size limit")
				return
			}
			Error(w, r, http.StatusBadRequest, "invalid_request", "invalid multipart body")
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()

		to := strings.TrimSpace(r.FormValue("to"))
		kind := strings.TrimSpace(r.FormValue("type"))
		caption := r.FormValue("caption")
		filename := strings.TrimSpace(r.FormValue("filename"))
		if to == "" {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "to is required")
			return
		}
		if !validMediaKind(kind) {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity",
				"type must be image, video, audio or document")
			return
		}
		ptt, err := parseFormBool(r.FormValue("ptt"))
		if err != nil {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "ptt must be a boolean")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file is required")
			return
		}
		defer func() { _ = file.Close() }()

		mimetype, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
		if err != nil {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file type is not supported")
			return
		}
		fileKind, ok := media.Kind(mimetype)
		if !ok {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file type is not supported")
			return
		}
		if fileKind != kind {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity",
				"type does not match the file content type")
			return
		}
		if filename == "" {
			filename = header.Filename
		}

		data, err := readMediaUpload(file, maxBytes)
		if err != nil {
			if errors.Is(err, media.ErrTooLarge) {
				Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file exceeds the size limit")
				return
			}
			Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		if len(data) == 0 {
			Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "file is empty")
			return
		}

		stored, err := mediaStore.Save(r.Context(), id, mediaDirectionOutbound, "", mimetype, filename, data)
		if err != nil {
			writeMediaUploadError(w, r, err)
			return
		}

		messageID, err := messages.Enqueue(r.Context(), id, message.EnqueueInput{
			Type:     message.TypeMedia,
			To:       to,
			Caption:  caption,
			Filename: stored.Filename,
			PTT:      ptt,
			MediaID:  &stored.ID,
		})
		if err != nil {
			writeMessageError(w, r, err)
			return
		}
		JSON(w, http.StatusAccepted, newMessageAcceptedResponse(messageID))
	}
}

// validMediaKind reports whether kind is one of the outbound media kinds.
func validMediaKind(kind string) bool {
	switch kind {
	case media.KindImage, media.KindVideo, media.KindAudio, media.KindDocument:
		return true
	}
	return false
}

// parseFormBool reads an optional form boolean: "true"/"1" are true, an empty
// value, "false" and "0" are false and anything else is an error.
func parseFormBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	}
	return false, errors.New("invalid boolean")
}

// readMediaUpload reads the file part, refusing content above the configured
// limit before it is buffered. A non-positive limit defers to the store.
func readMediaUpload(file io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(file)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, media.ErrTooLarge
	}
	return data, nil
}

// writeMediaUploadError maps a media store failure during an upload: empty and
// oversized files are unprocessable and an unknown instance surfaces as
// ErrNotFound from the media foreign key.
func writeMediaUploadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, media.ErrEmpty), errors.Is(err, media.ErrTooLarge):
		Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid media file")
	case errors.Is(err, media.ErrNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// handleGetMessage answers 200 with one message of the instance.
func handleGetMessage(messages MessageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		messageID, err := uuid.Parse(r.PathValue("message_id"))
		if err != nil {
			Error(w, r, http.StatusNotFound, "not_found", "message not found")
			return
		}

		found, err := messages.Get(r.Context(), id, messageID)
		if err != nil {
			writeMessageError(w, r, err)
			return
		}
		JSON(w, http.StatusOK, newMessageResponse(found))
	}
}

// handleListMessages answers one page of messages with its next cursor.
func handleListMessages(messages MessageService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := instanceID(w, r)
		if !ok {
			return
		}

		items, next, err := messages.List(r.Context(), id,
			parseMessagesLimit(r.URL.Query().Get("limit")), r.URL.Query().Get("cursor"))
		if err != nil {
			writeMessageError(w, r, err)
			return
		}

		response := messageListResponse{Items: make([]messageResponse, 0, len(items)), NextCursor: next}
		for i := range items {
			response.Items = append(response.Items, newMessageResponse(&items[i]))
		}
		JSON(w, http.StatusOK, response)
	}
}

// parseMessagesLimit reads the limit query parameter with the message defaults.
func parseMessagesLimit(raw string) int {
	return parseLimit(raw, defaultMessagesLimit, maxMessagesLimit)
}

// newMessageAcceptedResponse maps an accepted message id to its 202 body.
func newMessageAcceptedResponse(messageID uuid.UUID) messageAcceptedResponse {
	return messageAcceptedResponse{MessageID: messageID.String(), Status: message.StatusQueued}
}

// newMessageResponse maps a stored message to its JSON representation.
func newMessageResponse(msg *model.OutboundMessage) messageResponse {
	return messageResponse{
		ID:                msg.ID.String(),
		InstanceID:        msg.InstanceID.String(),
		Type:              msg.Type,
		Recipient:         msg.RecipientJID,
		Status:            msg.Status,
		WhatsAppMessageID: msg.WhatsAppMessageID,
		LastError:         msg.LastError,
		Attempts:          msg.Attempts,
		DeliveredAt:       msg.DeliveredAt,
		ReadAt:            msg.ReadAt,
		CreatedAt:         msg.CreatedAt,
		UpdatedAt:         msg.UpdatedAt,
	}
}

// writeMessageError maps a message service error to its HTTP status and error
// envelope. Unknown failures answer 500 without leaking their cause.
func writeMessageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, message.ErrInstanceNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "instance not found")
	case errors.Is(err, message.ErrMessageNotFound):
		Error(w, r, http.StatusNotFound, "not_found", "message not found")
	case errors.Is(err, message.ErrInstanceNotConnected):
		Error(w, r, http.StatusConflict, "conflict", "instance not connected")
	case errors.Is(err, message.ErrNumberNotFound):
		Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "recipient number is not on WhatsApp")
	case errors.Is(err, message.ErrInvalidInput):
		Error(w, r, http.StatusUnprocessableEntity, "unprocessable_entity", "invalid message content")
	case errors.Is(err, message.ErrResolverUnavailable):
		Error(w, r, http.StatusServiceUnavailable, "unavailable", "number resolution unavailable")
	case errors.Is(err, message.ErrInvalidCursor):
		Error(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
	default:
		Error(w, r, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
