package events

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// TestNATSPublisherReadyDisconnected checks that Ready fails while the broker
// connection is not established; the unreachable address avoids a live broker.
func TestNATSPublisherReadyDisconnected(t *testing.T) {
	conn, err := nats.Connect("nats://127.0.0.1:1",
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(1),
		nats.ReconnectWait(time.Second),
		nats.Timeout(250*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	publisher, err := NewNATSPublisher(conn, "WZAP_TEST", 7)
	if err != nil {
		t.Fatalf("NewNATSPublisher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := publisher.Ready(ctx); err == nil {
		t.Fatal("Ready succeeded while the broker is unreachable")
	}
}

// TestNATSPublisherIntegration exercises stream creation and publishing against
// a real JetStream server. Set WZAP_TEST_NATS_URL to run it; it reconciles the
// production stream name WZAP on the opted-in broker.
func TestNATSPublisherIntegration(t *testing.T) {
	url := os.Getenv("WZAP_TEST_NATS_URL")
	if url == "" {
		t.Skip("set WZAP_TEST_NATS_URL to run NATS integration tests")
	}
	const stream = "WZAP"

	conn, err := nats.Connect(url, nats.Name("wzap-events-test"), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	publisher, err := NewNATSPublisher(conn, stream, 7)
	if err != nil {
		t.Fatalf("NewNATSPublisher: %v", err)
	}

	if err := publisher.EnsureStream(ctx); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	if err := publisher.EnsureStream(ctx); err != nil {
		t.Fatalf("EnsureStream on an existing stream: %v", err)
	}

	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	info, err := js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		t.Fatalf("StreamInfo: %v", err)
	}
	if len(info.Config.Subjects) != 1 || info.Config.Subjects[0] != "wzap.>" {
		t.Errorf("stream subjects = %v, want [wzap.>]", info.Config.Subjects)
	}
	if want := 7 * 24 * time.Hour; info.Config.MaxAge != want {
		t.Errorf("stream MaxAge = %v, want %v", info.Config.MaxAge, want)
	}

	env := mustEnvelope(t, uuid.New(), "connection")
	subject := Subjects.Connection(env.InstanceID)
	if err := publisher.Publish(ctx, subject, env); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	raw, err := js.GetLastMsg(stream, subject, nats.Context(ctx))
	if err != nil {
		t.Fatalf("GetLastMsg: %v", err)
	}
	if raw.Subject != subject {
		t.Errorf("stored subject = %q, want %q", raw.Subject, subject)
	}
	if got := raw.Header.Get(nats.MsgIdHdr); got != env.EventID.String() {
		t.Errorf("stored Nats-Msg-Id = %q, want %q", got, env.EventID)
	}

	var decoded Envelope
	if err := json.Unmarshal(raw.Data, &decoded); err != nil {
		t.Fatalf("decode stored envelope: %v", err)
	}
	if decoded.EventID != env.EventID || decoded.EventVersion != env.EventVersion {
		t.Errorf("stored envelope = %+v, want id %s and version %d", decoded, env.EventID, env.EventVersion)
	}

	before := streamMsgCount(t, js, stream, ctx)
	if err := publisher.Publish(ctx, subject, env); err != nil {
		t.Fatalf("duplicate publish: %v", err)
	}
	if after := streamMsgCount(t, js, stream, ctx); after != before {
		t.Errorf("stored messages = %d after a duplicate event, want %d (Nats-Msg-Id must dedupe)", after, before)
	}
}

func streamMsgCount(t *testing.T, js nats.JetStreamContext, stream string, ctx context.Context) uint64 {
	t.Helper()

	info, err := js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		t.Fatalf("StreamInfo: %v", err)
	}
	return info.State.Msgs
}
