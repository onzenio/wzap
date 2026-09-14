package inbound

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"wzap/internal/chatwoot/client"
)

// ChatwootAPI is the Chatwoot surface the inbound needs for private notes
// and operational confirmations.
type ChatwootAPI interface {
	CreateMessage(ctx context.Context, conversationID int64, req client.CreateMessageRequest) (*client.Message, error)
}

// APIChats adapts a Chatwoot client to Chats: notes and confirmations go as
// outgoing messages, private exactly for failure notes.
type APIChats struct {
	api ChatwootAPI
}

// NewAPIChats builds Chats over api.
func NewAPIChats(api ChatwootAPI) *APIChats {
	return &APIChats{api: api}
}

// CreateMessage posts content to conversationID, private for failure notes.
func (a *APIChats) CreateMessage(ctx context.Context, conversationID int64, content string, private bool) (int64, error) {
	if a.api == nil {
		return 0, fmt.Errorf("chatwoot api unavailable")
	}
	msg, err := a.api.CreateMessage(ctx, conversationID, client.CreateMessageRequest{
		Content:     content,
		MessageType: client.MessageTypeOutgoing,
		Private:     private,
	})
	if err != nil {
		return 0, err
	}
	if msg == nil {
		return 0, nil
	}
	return msg.ID, nil
}

// HTTPDownloader fetches attachment bytes from data_url with a size cap.
type HTTPDownloader struct {
	client   *http.Client
	maxBytes int64
}

// NewHTTPDownloader builds a Downloader with a 15s timeout and maxBytes cap.
// A non-positive maxBytes defers the cap to the media store.
func NewHTTPDownloader(maxBytes int64) *HTTPDownloader {
	return &HTTPDownloader{client: &http.Client{Timeout: 15 * time.Second}, maxBytes: maxBytes}
}

// Download GETs url and returns its bytes plus the response content type.
func (d *HTTPDownloader) Download(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	limit := d.maxBytes + 1
	if limit <= 1 {
		limit = 1 << 26
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, "", err
	}
	mime := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}
