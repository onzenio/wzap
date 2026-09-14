package mirror

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"wzap/internal/events"
)

// natsURL returns the test broker URL or skips the test.
func natsURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("WZAP_TEST_NATS_URL")
	if url == "" {
		t.Skip("set WZAP_TEST_NATS_URL to run NATS integration tests")
	}
	return url
}

func dialNATS(t *testing.T, url string) *nats.Conn {
	t.Helper()
	conn, err := nats.Connect(url, nats.Name("wzap-mirror-test"), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	return conn
}

func jetStream(t *testing.T, conn *nats.Conn) nats.JetStreamContext {
	t.Helper()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	return js
}

// ensureTestStream creates an isolated stream for the mirror consumer test.
// The subject namespace is unique per run so the shared test broker never
// overlaps with the production WZAP stream (or another run). It returns the
// stream name and the subscribe/publish subject prefix.
func ensureTestStream(t *testing.T, js nats.JetStreamContext) (string, string) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	stream := "WZAP_TEST_MIRROR_" + strings.ToUpper(strings.ReplaceAll(suffix, "-", ""))
	// NOTE: the namespace deliberately stays outside "wzap.>" — the shared
	// test broker already carries a stream on "wzap.>" and JetStream rejects
	// overlapping stream subjects.
	namespace := "wzaptestmirror." + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := &nats.StreamConfig{
		// Per-run subjects: leftovers from killed runs can never overlap
		// a future namespace.
		Name:     stream,
		Subjects: []string{namespace + ".>"},
		Storage:  nats.MemoryStorage,
	}
	if _, err := js.AddStream(cfg, nats.Context(ctx)); err != nil {
		t.Fatalf("ensure stream %s: %v", stream, err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = js.DeleteStream(stream, nats.Context(cctx))
	})
	return stream, namespace
}

// waitForConsumer polls until the durable mirror consumer exists.
func waitForConsumer(t *testing.T, js nats.JetStreamContext, stream string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := js.ConsumerInfo(stream, DurableConsumer, nats.Context(ctx))
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable consumer %q did not appear: %v", DurableConsumer, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// testMessageEnvelope builds a message envelope shaped like the app producer
// emits it.
func testMessageEnvelope(t *testing.T, instanceID uuid.UUID) events.Envelope {
	t.Helper()
	env, err := events.New("message", instanceID, map[string]any{
		"from_jid":   "5511999999999@s.whatsapp.net",
		"chat_jid":   "5511999999999@s.whatsapp.net",
		"is_group":   false,
		"message_id": "WA-MSG-NATS",
		"timestamp":  time.Now().UTC(),
		"type":       "text",
		"text":       "ping via nats",
	})
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	return env
}

// publishEnvelope stores env on subject with its event id for dedup.
func publishEnvelope(t *testing.T, js nats.JetStreamContext, subject string, env events.Envelope) {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.Publish(subject, data,
		nats.Context(ctx), nats.MsgId(env.EventID.String())); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
