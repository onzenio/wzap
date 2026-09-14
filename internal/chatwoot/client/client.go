// Package client is the hand-rolled Chatwoot HTTP client consumed by the
// contacts, conversations and mirror packages. It uses the standard library
// only, authenticates every request with the api_access_token header and maps
// non-2xx responses to *Error.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// timeout is the per-request timeout for every Chatwoot call.
const timeout = 15 * time.Second

// Client talks to one Chatwoot account.
type Client struct {
	baseURL   string
	token     string
	accountID string
	http      *http.Client
}

// New returns a client for baseURL (scheme + host, no trailing slash),
// authenticating with token against accountID.
func New(baseURL, token, accountID string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		accountID: accountID,
		http:      &http.Client{Timeout: timeout},
	}
}

// accountPath prefixes path with the account scope.
func (c *Client) accountPath(path string) string {
	return "/api/v1/accounts/" + c.accountID + path
}

// do executes one request and decodes the JSON response into out when out is
// not nil and the body is not empty. A non-2xx status maps to *Error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, out any) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("api_access_token", c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &Error{Status: resp.StatusCode, Body: truncate(string(raw), 2000)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("chatwoot: decode response: %w", err)
	}
	return nil
}

// doJSON executes one JSON request.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, reqBody any, out any) error {
	var body io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("chatwoot: encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	return c.do(ctx, method, path, query, body, "application/json", out)
}

// FindContactByPhone returns contacts matching phone through the filter API.
func (c *Client) FindContactByPhone(ctx context.Context, phone string) ([]Contact, error) {
	reqBody := map[string]any{
		"payload": []map[string]any{
			{
				"attribute_key":   "phone_number",
				"filter_operator": "contains",
				"values":          []string{phone},
			},
		},
	}
	var decoded struct {
		Payload []Contact `json:"payload"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.accountPath("/contacts/filter"), nil, reqBody, &decoded); err != nil {
		return nil, err
	}
	if decoded.Payload == nil {
		return []Contact{}, nil
	}
	return decoded.Payload, nil
}

// SearchContacts returns contacts matching a free-text query.
func (c *Client) SearchContacts(ctx context.Context, query string) ([]Contact, error) {
	values := url.Values{}
	values.Set("q", query)
	var decoded struct {
		Payload []Contact `json:"payload"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.accountPath("/contacts/search"), values, nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Payload == nil {
		return []Contact{}, nil
	}
	return decoded.Payload, nil
}

// CreateContact creates a contact.
func (c *Client) CreateContact(ctx context.Context, req CreateContactRequest) (*Contact, error) {
	var contact Contact
	if err := c.doJSON(ctx, http.MethodPost, c.accountPath("/contacts"), nil, req, &contact); err != nil {
		return nil, err
	}
	return &contact, nil
}

// UpdateContact updates mutable contact fields.
func (c *Client) UpdateContact(ctx context.Context, contactID int64, req UpdateContactRequest) (*Contact, error) {
	var contact Contact
	path := c.accountPath("/contacts/" + strconv.FormatInt(contactID, 10))
	if err := c.doJSON(ctx, http.MethodPut, path, nil, req, &contact); err != nil {
		return nil, err
	}
	return &contact, nil
}

// ListInboxes returns every inbox of the account.
func (c *Client) ListInboxes(ctx context.Context) ([]Inbox, error) {
	var decoded struct {
		Payload []Inbox `json:"payload"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.accountPath("/inboxes"), nil, nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Payload == nil {
		return []Inbox{}, nil
	}
	return decoded.Payload, nil
}

// CreateInbox creates an api-channel inbox.
func (c *Client) CreateInbox(ctx context.Context, req CreateInboxRequest) (*Inbox, error) {
	reqBody := map[string]any{
		"name": req.Name,
		"channel": map[string]any{
			"type":        "api",
			"webhook_url": req.WebhookURL,
		},
	}
	var inbox Inbox
	if err := c.doJSON(ctx, http.MethodPost, c.accountPath("/inboxes"), nil, reqBody, &inbox); err != nil {
		return nil, err
	}
	return &inbox, nil
}

// ListContactConversations returns the conversations of a contact.
func (c *Client) ListContactConversations(ctx context.Context, contactID int64) ([]Conversation, error) {
	path := c.accountPath("/contacts/" + strconv.FormatInt(contactID, 10) + "/conversations")
	var conversations []Conversation
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &conversations); err != nil {
		return nil, err
	}
	if conversations == nil {
		return []Conversation{}, nil
	}
	return conversations, nil
}

// GetConversation returns one conversation by id.
func (c *Client) GetConversation(ctx context.Context, conversationID int64) (*Conversation, error) {
	var conversation Conversation
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &conversation); err != nil {
		return nil, err
	}
	return &conversation, nil
}

// CreateConversation opens a conversation for a contact in an inbox.
func (c *Client) CreateConversation(ctx context.Context, req CreateConversationRequest) (*Conversation, error) {
	var conversation Conversation
	if err := c.doJSON(ctx, http.MethodPost, c.accountPath("/conversations"), nil, req, &conversation); err != nil {
		return nil, err
	}
	return &conversation, nil
}

// ToggleConversationStatus moves a conversation to status
// (open, resolved, pending or snoozed).
func (c *Client) ToggleConversationStatus(ctx context.Context, conversationID int64, status string) (*Conversation, error) {
	var conversation Conversation
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10) + "/toggle_status")
	reqBody := map[string]any{"status": status}
	if err := c.doJSON(ctx, http.MethodPost, path, nil, reqBody, &conversation); err != nil {
		return nil, err
	}
	return &conversation, nil
}

// CreateMessage posts a text-only message to a conversation.
func (c *Client) CreateMessage(ctx context.Context, conversationID int64, req CreateMessageRequest) (*Message, error) {
	var message Message
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10) + "/messages")
	if err := c.doJSON(ctx, http.MethodPost, path, nil, req, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

// CreateMessageWithAttachment posts a message with a single file attachment
// via multipart form (attachments[], content, message_type,
// content_attributes JSON, source_id, source_reply_id).
func (c *Client) CreateMessageWithAttachment(ctx context.Context, conversationID int64, req CreateMessageWithAttachmentRequest) (*Message, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if req.Content != "" {
		_ = writer.WriteField("content", req.Content)
	}
	if req.MessageType != "" {
		_ = writer.WriteField("message_type", req.MessageType)
	}
	if req.Private {
		_ = writer.WriteField("private", "true")
	}
	if len(req.ContentAttributes) > 0 {
		raw, err := json.Marshal(req.ContentAttributes)
		if err != nil {
			return nil, fmt.Errorf("chatwoot: encode content_attributes: %w", err)
		}
		_ = writer.WriteField("content_attributes", string(raw))
	}
	if req.SourceID != "" {
		_ = writer.WriteField("source_id", req.SourceID)
	}
	if req.SourceReplyID != "" {
		_ = writer.WriteField("source_reply_id", req.SourceReplyID)
	}
	if len(req.File) > 0 {
		name := req.FileName
		if name == "" {
			name = "attachment"
		}
		contentType := req.ContentType
		if contentType == "" {
			contentType = http.DetectContentType(req.File)
			if contentType == "" {
				contentType = "application/octet-stream"
			}
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes("attachments[]"), escapeQuotes(name)))
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, fmt.Errorf("chatwoot: create form file: %w", err)
		}
		if _, err := part.Write(req.File); err != nil {
			return nil, fmt.Errorf("chatwoot: write attachment: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("chatwoot: close multipart: %w", err)
	}
	var message Message
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10) + "/messages")
	if err := c.do(ctx, http.MethodPost, path, nil, &buf, writer.FormDataContentType(), &message); err != nil {
		return nil, err
	}
	return &message, nil
}

// DeleteMessage deletes one message from a conversation.
func (c *Client) DeleteMessage(ctx context.Context, conversationID, messageID int64) (struct{}, error) {
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10) +
		"/messages/" + strconv.FormatInt(messageID, 10))
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

// MergeContacts merges mergee into base, keeping base.
func (c *Client) MergeContacts(ctx context.Context, req MergeContactsRequest) (*Contact, error) {
	var contact Contact
	if err := c.doJSON(ctx, http.MethodPost, c.accountPath("/actions/contact_merge"), nil, req, &contact); err != nil {
		return nil, err
	}
	return &contact, nil
}

// UpdateLastSeen marks a conversation as seen.
func (c *Client) UpdateLastSeen(ctx context.Context, conversationID int64) (struct{}, error) {
	path := c.accountPath("/conversations/" + strconv.FormatInt(conversationID, 10) + "/update_last_seen")
	if err := c.doJSON(ctx, http.MethodPost, path, nil, map[string]any{}, nil); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// escapeQuotes escapes backslashes and double quotes as mime/multipart does
// for Content-Disposition filenames.
func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
