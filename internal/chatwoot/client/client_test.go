package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateMessagePostsAndDecodesID(t *testing.T) {
	var gotMethod, gotPath, gotToken string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("api_access_token")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99, "content": "hello"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token", "1")
	msg, err := c.CreateMessage(context.Background(), 7, CreateMessageRequest{
		Content:     "hello",
		MessageType: "outgoing",
		SourceID:    "WAID:abc",
	})
	if err != nil {
		t.Fatalf("CreateMessage = %v, want nil", err)
	}
	if msg.ID != 99 {
		t.Errorf("message id = %d, want 99", msg.ID)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/accounts/1/conversations/7/messages"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotToken != "test-token" {
		t.Errorf("api_access_token = %q, want %q", gotToken, "test-token")
	}
	if gotBody["content"] != "hello" {
		t.Errorf("body content = %v, want hello", gotBody["content"])
	}
}

func TestErrorMapsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"bad"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token", "1")
	_, err := c.GetConversation(context.Background(), 7)
	cerr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if cerr.Status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", cerr.Status)
	}
}

func TestCreateMessageWithAttachmentSendsMultipart(t *testing.T) {
	var gotContentType, gotToken string
	var gotFormContent, gotMessageType, gotSourceID string
	var gotAttachmentNames []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotToken = r.Header.Get("api_access_token")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotFormContent = r.FormValue("content")
		gotMessageType = r.FormValue("message_type")
		gotSourceID = r.FormValue("source_id")
		if r.MultipartForm != nil && r.MultipartForm.File != nil {
			for _, files := range r.MultipartForm.File {
				for _, fh := range files {
					gotAttachmentNames = append(gotAttachmentNames, fh.Filename)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 55})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token", "1")
	msg, err := c.CreateMessageWithAttachment(context.Background(), 7, CreateMessageWithAttachmentRequest{
		Content:     "photo",
		MessageType: "outgoing",
		SourceID:    "WAID:abc",
		FileName:    "photo.jpg",
		ContentType: "image/jpeg",
		File:        []byte("fake-bytes"),
	})
	if err != nil {
		t.Fatalf("CreateMessageWithAttachment = %v, want nil", err)
	}
	if msg.ID != 55 {
		t.Errorf("message id = %d, want 55", msg.ID)
	}
	if gotToken != "test-token" {
		t.Errorf("api_access_token = %q, want %q", gotToken, "test-token")
	}
	if gotFormContent != "photo" || gotMessageType != "outgoing" || gotSourceID != "WAID:abc" {
		t.Errorf("form fields = %q/%q/%q, want photo/outgoing/WAID:abc", gotFormContent, gotMessageType, gotSourceID)
	}
	if len(gotAttachmentNames) == 0 {
		t.Error("no attachment file part received")
	}
	_ = gotContentType
}
