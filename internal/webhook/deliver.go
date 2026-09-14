package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// deliverTimeout pins the webhook POST deadline: every delivery must finish
// within 5s.
const deliverTimeout = 5 * time.Second

// webhookClient pins the delivery timeout and the SSRF guard: CheckRedirect
// revalida cada hop contra o gate (cap em maxWebhookRedirects) e o
// DialContext resolve uma vez, valida e disca o IP validado (pinning,
// sem TOCTOU entre validate e connect).
var webhookClient = &http.Client{
	Timeout:       deliverTimeout,
	CheckRedirect: checkWebhookRedirect,
	Transport: &http.Transport{
		DialContext:           pinnedDialer,
		ResponseHeaderTimeout: deliverTimeout,
	},
}

// ErrSkipped reports a delivery that must not be attempted: the webhook is
// disabled, has no URL, or does not subscribe the event type. It is distinct
// from a delivery error on purpose — the 4.3 worker drops ErrSkipped without
// retry or dead-letter, while any other error is a failed attempt. Callers
// are expected to check ShouldDeliver first; the empty-URL guard here is the
// defensive floor under that gate.
var ErrSkipped = errors.New("webhook: delivery skipped")

// Deliver POSTs payload as JSON to the instance webhook URL with the vigente
// instance key in the apikey header.
//
// There is deliberately no HMAC or signature: confidentiality rests on HTTPS,
// enforced at config time by ValidateURL (plain http only for loopback
// hosts). That trade-off is accepted.
//
// An empty URL sends nothing and returns ErrSkipped. An empty key sends
// nothing and returns an error (the "sem key" path): a cache miss, a revoked
// key, or a restart-cold cache must fail recorded, never anonymously
// delivered. A 2xx status is success; anything else (network error, timeout,
// non-2xx) is an error naming the URL and the status. Errors never contain
// the key.
func Deliver(ctx context.Context, url, key string, payload []byte) error {
	if url == "" {
		return fmt.Errorf("%w: no webhook url", ErrSkipped)
	}
	if err := validateWebhookURL(url); err != nil {
		return fmt.Errorf("webhook deliver to %s: %w", url, err)
	}
	if key == "" {
		return fmt.Errorf("webhook deliver to %s: no instance key cached", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webhook deliver to %s: build request: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", key)

	resp, err := webhookClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook deliver to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain at most 1 MiB: the body is irrelevant past the status code and
	// a hostile endpoint must not make us buffer unboundedly.
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); err != nil {
		return fmt.Errorf("webhook deliver to %s: drain response: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook deliver to %s: unexpected status %s", url, resp.Status)
	}
	return nil
}

// CutRawForLimit trims oversized string values out of a raw JSON event so the
// webhook body stays bounded: every string longer than limitBytes is replaced
// by the omission marker {"omitted":true,"size_bytes":<original length>}. When
// nothing crosses the limit the input is returned byte-verbatim (no
// re-serialization), so numbers, spacing and key order survive untouched.
//
// Callers pass the service's MaxMediaBytes (WZAP_MAX_MEDIA_BYTES, 16 MiB) as
// the limit: the same cap that bounds stored media. Cut blobs need no
// replacement inline because the media itself still travels via the envelope
// media URL (/media/{id}); only the inline copy is cut.
//
// The cut line is the Go string length in bytes — not the base64-decoded size
// — a conservative, deterministic rule that needs no MIME awareness.
//
// An empty or nil raw returns as-is with cut=false. Non-JSON input is not an
// error: it passes through unchanged with cut=false so trimming can never
// fail a delivery; the boolean reports whether anything was cut.
func CutRawForLimit(raw []byte, limitBytes int64) (trimmed []byte, cut bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var value any
	// UseNumber keeps every numeric literal verbatim through the
	// re-marshal: ids and timestamps beyond float64 precision must survive
	// the walk bit-for-bit. The trailing decode keeps Unmarshal's strictness
	// (no trailing data after the single JSON value).
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return raw, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return raw, false
	}
	trimmedValue, cut := cutValue(value, limitBytes)
	if !cut {
		return raw, false
	}
	out, err := json.Marshal(trimmedValue)
	if err != nil {
		return raw, false
	}
	return out, cut
}

// cutValue walks decoded JSON, replacing oversized strings with the omission
// marker. It reports whether any replacement happened.
func cutValue(value any, limitBytes int64) (any, bool) {
	switch typed := value.(type) {
	case string:
		if int64(len(typed)) > limitBytes {
			return map[string]any{"omitted": true, "size_bytes": len(typed)}, true
		}
		return typed, false
	case map[string]any:
		var cut bool
		for key, item := range typed {
			trimmed, itemCut := cutValue(item, limitBytes)
			if itemCut {
				typed[key] = trimmed
				cut = true
			}
		}
		return typed, cut
	case []any:
		var cut bool
		for i, item := range typed {
			trimmed, itemCut := cutValue(item, limitBytes)
			if itemCut {
				typed[i] = trimmed
				cut = true
			}
		}
		return typed, cut
	default:
		return value, false
	}
}
