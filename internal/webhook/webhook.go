// Package webhook validates the per-instance webhook configuration and
// canonicalizes the subscribed event types. An empty URL is always valid and
// means "no webhook": it is stored unset and never produces a delivery, even
// with enabled set. Validation applies only to non-empty URLs.
package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrInvalid reports a webhook configuration the service must reject. Handlers
// map every error wrapping it to 422 unprocessable_entity without persisting
// anything.
var ErrInvalid = errors.New("invalid webhook config")

// canonicalEvents is the only accepted subscription set, in the deterministic
// order stored regardless of input order.
var canonicalEvents = []string{"message", "receipt", "connection", "message.status"}

// canonicalSet is the exact lowercase membership of canonicalEvents: matching
// is case-sensitive ("Message" is rejected, not normalized).
var canonicalSet = map[string]bool{
	"message":        true,
	"receipt":        true,
	"connection":     true,
	"message.status": true,
}

// DefaultEvents returns the subscription used when webhook_events is omitted:
// all four canonical types in canonical order. The slice is a fresh copy.
func DefaultEvents() []string {
	return append([]string(nil), canonicalEvents...)
}

// ValidateURL checks a webhook URL. Empty stays valid and returns nil (unset,
// R22). A non-empty URL must parse, use scheme http or https, carry a host,
// and use http only for loopback hosts; https allows any host.
func ValidateURL(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed url: %v", ErrInvalid, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		host := parsed.Hostname()
		if host == "" {
			return nil, fmt.Errorf("%w: http url has no host", ErrInvalid)
		}
		if !isLoopbackHost(host) {
			return nil, fmt.Errorf("%w: http is allowed only for loopback hosts", ErrInvalid)
		}
	case "https":
		if parsed.Hostname() == "" {
			return nil, fmt.Errorf("%w: https url has no host", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrInvalid)
	}
	url := raw
	return &url, nil
}

// ValidateEvents canonicalizes an explicit webhook_events list: unknown types
// fail, duplicates collapse to one, and the stored order is the canonical one
// rather than the input order. An explicit empty list is valid and stays empty
// (it delivers nothing).
func ValidateEvents(events []string) ([]string, error) {
	present := make(map[string]bool, len(events))
	for _, event := range events {
		if !canonicalSet[event] {
			return nil, fmt.Errorf("%w: unknown event type %q", ErrInvalid, event)
		}
		present[event] = true
	}
	normalized := make([]string, 0, len(canonicalEvents))
	for _, event := range canonicalEvents {
		if present[event] {
			normalized = append(normalized, event)
		}
	}
	return normalized, nil
}

// ValidateConfig validates a webhook configuration with create semantics: an
// empty rawURL stays unset (nil) and a nil events pointer — webhook_events
// omitted — defaults to all canonical types, while an explicit (possibly
// empty) list is canonicalized as-is. Partial updates must instead validate
// only the fields present via ValidateURL/ValidateEvents, so an absent events
// pointer there keeps the stored subscription instead of resetting it.
func ValidateConfig(rawURL string, events *[]string) (*string, []string, error) {
	normalizedURL, err := ValidateURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if events == nil {
		return normalizedURL, DefaultEvents(), nil
	}
	normalized, err := ValidateEvents(*events)
	if err != nil {
		return nil, nil, err
	}
	return normalizedURL, normalized, nil
}

// isLoopbackHost reports whether host is a loopback destination without
// resolving anything via DNS: "localhost" (case-insensitive, optional trailing
// dot) or a literal IP in 127.0.0.0/8 or ::1.
func isLoopbackHost(host string) bool {
	trimmed := strings.TrimSuffix(host, ".")
	if strings.EqualFold(trimmed, "localhost") {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
