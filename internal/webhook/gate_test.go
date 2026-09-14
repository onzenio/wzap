package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubscribed(t *testing.T) {
	if !Subscribed([]string{"message", "receipt"}, "receipt") {
		t.Error("Subscribed(list, receipt) = false, want true")
	}
	if Subscribed([]string{"message"}, "receipt") {
		t.Error("Subscribed(list, receipt) = true, want false for an unlisted type")
	}
	if Subscribed(nil, "message") {
		t.Error("Subscribed(nil, message) = true, want false")
	}
	if Subscribed([]string{"Message"}, "message") {
		t.Error("Subscribed([Message], message) = true, want false (matching is case-sensitive)")
	}
}

func TestShouldDeliver(t *testing.T) {
	full := WebhookConfig{
		URL:     "https://hooks.example.com/wzap",
		Enabled: true,
		Events:  []string{"message", "receipt", "connection", "message.status"},
	}

	tests := []struct {
		name      string
		cfg       WebhookConfig
		eventType string
		want      bool
	}{
		{name: "enabled with url and subscribed type delivers", cfg: full, eventType: "message", want: true},
		{name: "every canonical type delivers when subscribed", cfg: full, eventType: "message.status", want: true},
		{
			name:      "disabled delivers nothing",
			cfg:       WebhookConfig{URL: full.URL, Enabled: false, Events: full.Events},
			eventType: "message",
			want:      false,
		},
		{
			name:      "empty url delivers nothing",
			cfg:       WebhookConfig{URL: "", Enabled: true, Events: full.Events},
			eventType: "message",
			want:      false,
		},
		{
			name:      "unsubscribed type delivers nothing",
			cfg:       WebhookConfig{URL: full.URL, Enabled: true, Events: []string{"message"}},
			eventType: "receipt",
			want:      false,
		},
		{
			name:      "explicit empty subscription delivers nothing",
			cfg:       WebhookConfig{URL: full.URL, Enabled: true, Events: nil},
			eventType: "message",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldDeliver(tt.cfg, tt.eventType); got != tt.want {
				t.Errorf("ShouldDeliver(%+v, %q) = %v, want %v", tt.cfg, tt.eventType, got, tt.want)
			}
		})
	}
}

// TestGateBlocksDeliverWithZeroRequests wires the gate the way the 4.3 worker
// must: Deliver is only called when ShouldDeliver passes, so a disabled,
// URL-less or unsubscribed delivery never produces an HTTP request.
func TestGateBlocksDeliverWithZeroRequests(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	gated := []struct {
		name      string
		cfg       WebhookConfig
		eventType string
	}{
		{
			name:      "disabled",
			cfg:       WebhookConfig{URL: srv.URL, Enabled: false, Events: []string{"message"}},
			eventType: "message",
		},
		{
			name:      "no url",
			cfg:       WebhookConfig{URL: "", Enabled: true, Events: []string{"message"}},
			eventType: "message",
		},
		{
			name:      "unsubscribed type",
			cfg:       WebhookConfig{URL: srv.URL, Enabled: true, Events: []string{"message"}},
			eventType: "receipt",
		},
	}

	for _, tt := range gated {
		t.Run(tt.name, func(t *testing.T) {
			if ShouldDeliver(tt.cfg, tt.eventType) {
				t.Fatalf("ShouldDeliver(%+v, %q) = true, want false", tt.cfg, tt.eventType)
			}
			// The 4.3 call pattern: the gate owns the skip, Deliver is
			// never reached.
		})
	}
	if got := r.requests.Load(); got != 0 {
		t.Errorf("requests received = %d, want 0 (gated deliveries never POST)", got)
	}

	// Control: a subscribed, enabled delivery with a URL passes the gate and
	// posts exactly once.
	allowed := WebhookConfig{URL: srv.URL, Enabled: true, Events: []string{"message"}}
	if !ShouldDeliver(allowed, "message") {
		t.Fatal("ShouldDeliver(allowed, message) = false, want true")
	}
	if err := Deliver(context.Background(), allowed.URL, "gate-control-key", []byte(`{"type":"message"}`)); err != nil {
		t.Fatalf("Deliver(allowed): %v", err)
	}
	if got := r.requests.Load(); got != 1 {
		t.Errorf("requests received = %d, want the single allowed POST", got)
	}
}
