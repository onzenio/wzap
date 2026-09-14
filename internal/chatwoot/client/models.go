package client

// Message type values accepted by the Chatwoot messages API.
const (
	MessageTypeIncoming = "incoming"
	MessageTypeOutgoing = "outgoing"
)

// Error reports a non-2xx Chatwoot response. Status is the HTTP status code
// and Body is the raw response body, truncated for readability.
type Error struct {
	Status int
	Body   string
}

// Error implements the error interface.
func (e *Error) Error() string {
	return "chatwoot: unexpected status " + itoa(e.Status) + ": " + e.Body
}

// Contact is the minimal Chatwoot contact projection used by the mirror.
type Contact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Email       string `json:"email,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// CreateContactRequest creates a contact. PhoneNumber carries the leading
// "+" form; Identifier carries group JIDs.
type CreateContactRequest struct {
	Name        string `json:"name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Email       string `json:"email,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// UpdateContactRequest updates mutable contact fields.
type UpdateContactRequest struct {
	Name        string `json:"name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Email       string `json:"email,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// Inbox is the minimal Chatwoot inbox projection.
type Inbox struct {
	ID          int64  `json:"id"`
	Name        string `json:"name,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
	WebhookURL  string `json:"webhook_url,omitempty"`
}

// CreateInboxRequest creates an api-channel inbox.
type CreateInboxRequest struct {
	Name       string `json:"name"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

// Conversation is the minimal Chatwoot conversation projection.
type Conversation struct {
	ID      int64  `json:"id"`
	Status  string `json:"status,omitempty"`
	InboxID int64  `json:"inbox_id,omitempty"`
}

// CreateConversationRequest opens a conversation for a contact in an inbox.
type CreateConversationRequest struct {
	SourceID  string `json:"source_id"`
	InboxID   int64  `json:"inbox_id"`
	ContactID int64  `json:"contact_id,omitempty"`
	Status    string `json:"status,omitempty"`
}

// Message is the minimal Chatwoot message projection.
type Message struct {
	ID          int64  `json:"id"`
	Content     string `json:"content,omitempty"`
	MessageType string `json:"message_type,omitempty"`
}

// CreateMessageRequest posts a text-only message.
type CreateMessageRequest struct {
	Content           string         `json:"content,omitempty"`
	MessageType       string         `json:"message_type,omitempty"`
	Private           bool           `json:"private,omitempty"`
	ContentAttributes map[string]any `json:"content_attributes,omitempty"`
	SourceID          string         `json:"source_id,omitempty"`
	SourceReplyID     string         `json:"source_reply_id,omitempty"`
}

// CreateMessageWithAttachmentRequest posts a message with a single file
// attachment via multipart form.
type CreateMessageWithAttachmentRequest struct {
	Content           string
	MessageType       string
	Private           bool
	ContentAttributes map[string]any
	SourceID          string
	SourceReplyID     string
	FileName          string
	ContentType       string
	File              []byte
}

// MergeContactsRequest merges mergee into base, keeping base.
type MergeContactsRequest struct {
	BaseContactID   int64 `json:"base_contact_id"`
	MergeeContactID int64 `json:"mergee_contact_id"`
}
