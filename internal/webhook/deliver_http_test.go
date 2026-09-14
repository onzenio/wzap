package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
)

// receptor is an httptest webhook endpoint recording what Deliver sent.
type receptor struct {
	t          *testing.T
	header     atomic.Value // string
	body       atomic.Value // []byte
	requests   atomic.Int32
	status     int
	blockUntil chan struct{}
}

func newReceptor(t *testing.T, status int) *receptor {
	t.Helper()
	r := &receptor{status: status}
	r.header.Store("")
	r.body.Store([]byte(nil))
	return r
}

func (r *receptor) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.blockUntil != nil {
			<-r.blockUntil
		}
		r.requests.Add(1)
		r.header.Store(req.Header.Get("apikey"))
		// Read the full body regardless of ContentLength framing.
		var buf strings.Builder
		tmp := make([]byte, 4096)
		for {
			n, err := req.Body.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				break
			}
		}
		r.body.Store([]byte(buf.String()))
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			r.t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(r.status)
	}
}

func (r *receptor) gotHeader() string {
	v, _ := r.header.Load().(string)
	return v
}

func (r *receptor) gotBody() string {
	v, _ := r.body.Load().([]byte)
	return string(v)
}

// TestDeliverSendsHeaderAndBody pins the receptor contract: the instance key
// travels in the apikey header and the payload goes out byte-exact.
func TestDeliverSendsHeaderAndBody(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent} {
		r := newReceptor(t, status)
		srv := httptest.NewServer(r.handler())
		t.Cleanup(srv.Close)

		const key = "test-instance-key-header-body"
		const payload = `{"event_id":"abc","type":"message"}`
		if err := Deliver(context.Background(), srv.URL, key, []byte(payload)); err != nil {
			t.Fatalf("Deliver (status %d): %v", status, err)
		}
		if got := r.gotHeader(); got != key {
			t.Errorf("status %d: apikey header = %q, want the exact instance key", status, got)
		}
		if got := r.gotBody(); got != payload {
			t.Errorf("status %d: body = %q, want %q", status, got, payload)
		}
	}
}

// TestDeliverEnvelopeTwiceIsByteIdentical pins the at-least-once contract
// end to end: a real events.Envelope with its raw event rides the webhook
// body, and redelivering the same payload yields byte-identical bodies with
// the stable event_id the receiver dedupes on.
func TestDeliverEnvelopeTwiceIsByteIdentical(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	instanceID := uuid.New()
	env, err := events.New("message", instanceID, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	raw := json.RawMessage(`{"message":{"conversation":"hello"}}`)
	trimmed, cut := CutRawForLimit(raw, 16<<20)
	if cut {
		t.Fatalf("CutRawForLimit cut a small raw event, want it kept")
	}
	env.Event = trimmed

	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal envelope: %v", err)
	}

	const key = "test-instance-key-envelope-idempotent"
	if err := Deliver(context.Background(), srv.URL, key, payload); err != nil {
		t.Fatalf("Deliver first attempt: %v", err)
	}
	first := r.gotBody()
	if err := Deliver(context.Background(), srv.URL, key, payload); err != nil {
		t.Fatalf("Deliver second attempt: %v", err)
	}
	second := r.gotBody()

	if got := r.requests.Load(); got != 2 {
		t.Fatalf("requests received = %d, want 2 attempts", got)
	}
	if first != second {
		t.Errorf("redelivery bodies differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != string(payload) {
		t.Errorf("delivered body = %s, want the envelope payload %s", first, payload)
	}

	var body struct {
		EventID string          `json:"event_id"`
		Type    string          `json:"type"`
		Event   json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal([]byte(first), &body); err != nil {
		t.Fatalf("decode delivered body: %v", err)
	}
	if body.EventID != env.EventID.String() {
		t.Errorf("body.event_id = %q, want the stable %q", body.EventID, env.EventID)
	}
	if body.Type != "message" {
		t.Errorf("body.type = %q, want message", body.Type)
	}
	if string(body.Event) != string(raw) {
		t.Errorf("body.event = %s, want the raw event %s", body.Event, raw)
	}
	if got := r.gotHeader(); got != key {
		t.Errorf("apikey header = %q, want the instance key", got)
	}
}

// TestDeliverEmptyURLIsSkipped pins the defensive floor under the C1 gate: an
// empty URL never produces a request and fails with the ErrSkipped sentinel,
// distinct from a delivery error, so the 4.3 worker drops it without retry.
func TestDeliverEmptyURLIsSkipped(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	err := Deliver(context.Background(), "", "some-key", []byte(`{"type":"message"}`))
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("Deliver(empty url) error = %v, want ErrSkipped", err)
	}
	if got := r.requests.Load(); got != 0 {
		t.Errorf("requests received = %d, want 0 (empty URL never POSTs)", got)
	}
}

// TestDeliverNon2xxIsError pins failure on any non-2xx without exposing the key.
func TestDeliverNon2xxIsError(t *testing.T) {
	r := newReceptor(t, http.StatusInternalServerError)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	const key = "super-secret-test-key-500"
	err := Deliver(context.Background(), srv.URL, key, []byte(`{}`))
	if err == nil {
		t.Fatal("Deliver on 500: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error = %q, want it to name the URL", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error = %q, must never contain the key", err)
	}
}

// TestDeliverEmptyKeySendsNothing pins the "sem key" path: no request leaves
// the process and the error carries no key material.
func TestDeliverEmptyKeySendsNothing(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	err := Deliver(context.Background(), srv.URL, "", []byte(`{"type":"message"}`))
	if err == nil {
		t.Fatal("Deliver with empty key: error = nil, want the sem-key error")
	}
	if got := r.requests.Load(); got != 0 {
		t.Errorf("requests received = %d, want 0 (nothing is sent without a key)", got)
	}
}

// TestDeliverContextTimeout pins that Deliver honors cancellation: a slow
// receptor plus a short context deadline fails fast with no key in the error.
func TestDeliverContextTimeout(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	r.blockUntil = make(chan struct{})
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(r.blockUntil) })

	const key = "super-secret-test-key-timeout"
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Deliver(ctx, srv.URL, key, []byte(`{}`))
	if err == nil {
		t.Fatal("Deliver on expired context: error = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Deliver took %v, want it to fail fast on context expiry", elapsed)
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error = %q, must never contain the key", err)
	}
}

// TestDeliverNetworkErrorNamesURL pins that unreachable hosts fail with the
// URL named and the key absent.
func TestDeliverNetworkErrorNamesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens here now.

	const key = "super-secret-test-key-unreachable"
	err := Deliver(context.Background(), url, key, []byte(`{}`))
	if err == nil {
		t.Fatal("Deliver to a dead server: error = nil, want a network error")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error = %q, want it to name the URL", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error = %q, must never contain the key", err)
	}
}
