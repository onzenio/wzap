package webhook

import (
	"reflect"
	"testing"
)

// eventsPtr wraps events in a pointer so the table can express omitted (nil)
// versus explicit (non-nil, possibly empty) webhook_events.
func eventsPtr(events ...string) *[]string {
	if events == nil {
		empty := []string{}
		return &empty
	}
	return &events
}

func TestValidateConfig(t *testing.T) {
	all := []string{"message", "receipt", "connection", "message.status"}

	tests := []struct {
		name       string
		rawURL     string
		events     *[]string
		wantURL    *string
		wantEvents []string
		wantErr    bool
	}{
		{name: "https any host ok", rawURL: "https://hooks.example.com/wzap", events: nil, wantURL: strptr("https://hooks.example.com/wzap"), wantEvents: all},
		{name: "https with port and path ok", rawURL: "https://example.com:8443/hook?token=a", events: eventsPtr("message"), wantURL: strptr("https://example.com:8443/hook?token=a"), wantEvents: []string{"message"}},
		{name: "http localhost ok", rawURL: "http://localhost:8080/hook", events: nil, wantURL: strptr("http://localhost:8080/hook"), wantEvents: all},
		{name: "http 127.0.0.1 ok", rawURL: "http://127.0.0.1/hook", events: nil, wantURL: strptr("http://127.0.0.1/hook"), wantEvents: all},
		{name: "http 127.1.2.3 ok", rawURL: "http://127.1.2.3/hook", events: nil, wantURL: strptr("http://127.1.2.3/hook"), wantEvents: all},
		{name: "http ipv6 loopback ok", rawURL: "http://[::1]:8080/hook", events: nil, wantURL: strptr("http://[::1]:8080/hook"), wantEvents: all},
		{name: "http 192.168 rejected", rawURL: "http://192.168.1.10/hook", events: nil, wantErr: true},
		{name: "http 10.x rejected", rawURL: "http://10.0.0.5/hook", events: nil, wantErr: true},
		{name: "http public name rejected", rawURL: "http://example.com/hook", events: nil, wantErr: true},
		{name: "http empty host rejected", rawURL: "http:///hook", events: nil, wantErr: true},
		{name: "ftp rejected", rawURL: "ftp://example.com/hook", events: nil, wantErr: true},
		{name: "ws rejected", rawURL: "ws://example.com/hook", events: nil, wantErr: true},
		{name: "garbage rejected", rawURL: "not a url %%", events: nil, wantErr: true},
		{name: "scheme-less rejected", rawURL: "example.com/hook", events: nil, wantErr: true},
		// Ruling R22: an empty URL is always valid and stored unset, even
		// though delivery then never happens.
		{name: "empty url valid unset", rawURL: "", events: nil, wantURL: nil, wantEvents: all},
		{name: "empty url with explicit empty events", rawURL: "", events: eventsPtr(), wantURL: nil, wantEvents: []string{}},
		{name: "unknown type rejected", rawURL: "https://example.com/hook", events: eventsPtr("message", "bogus"), wantErr: true},
		{name: "omitted events default all", rawURL: "https://example.com/hook", events: nil, wantURL: strptr("https://example.com/hook"), wantEvents: all},
		{name: "explicit empty events stay empty", rawURL: "https://example.com/hook", events: eventsPtr(), wantURL: strptr("https://example.com/hook"), wantEvents: []string{}},
		{name: "dupes collapse", rawURL: "https://example.com/hook", events: eventsPtr("message", "message", "receipt"), wantURL: strptr("https://example.com/hook"), wantEvents: []string{"message", "receipt"}},
		{name: "stored order is canonical not input order", rawURL: "https://example.com/hook", events: eventsPtr("message.status", "message"), wantURL: strptr("https://example.com/hook"), wantEvents: []string{"message", "message.status"}},
		{name: "case-sensitive reject", rawURL: "https://example.com/hook", events: eventsPtr("Message"), wantErr: true},
		{name: "blank entry rejected", rawURL: "https://example.com/hook", events: eventsPtr(""), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotEvents, err := ValidateConfig(tt.rawURL, tt.events)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateConfig(%q, %v) error = nil, want an error", tt.rawURL, derefEvents(tt.events))
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateConfig(%q, %v): %v", tt.rawURL, derefEvents(tt.events), err)
			}
			if !reflect.DeepEqual(gotURL, tt.wantURL) {
				t.Errorf("URL = %v, want %v", derefURL(gotURL), derefURL(tt.wantURL))
			}
			if !reflect.DeepEqual(gotEvents, tt.wantEvents) {
				t.Errorf("events = %v, want %v", gotEvents, tt.wantEvents)
			}
		})
	}
}

func TestValidateURLEmptyStaysUnset(t *testing.T) {
	got, err := ValidateURL("")
	if err != nil {
		t.Fatalf("ValidateURL(%q): %v", "", err)
	}
	if got != nil {
		t.Errorf("ValidateURL(%q) = %q, want nil (unset)", "", *got)
	}
}

func TestDefaultEvents(t *testing.T) {
	want := []string{"message", "receipt", "connection", "message.status"}
	if got := DefaultEvents(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultEvents() = %v, want %v", got, want)
	}
}

func strptr(s string) *string { return &s }

func derefURL(url *string) string {
	if url == nil {
		return "<nil>"
	}
	return *url
}

func derefEvents(events *[]string) []string {
	if events == nil {
		return nil
	}
	return *events
}
