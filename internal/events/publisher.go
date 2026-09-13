package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Publisher delivers event envelopes to the durable broker stream.
type Publisher interface {
	// EnsureStream creates the stream when missing and reconciles its
	// configuration when it already exists.
	EnsureStream(ctx context.Context) error
	// Publish stores env under subject, deduplicated by envelope event id.
	Publish(ctx context.Context, subject string, env Envelope) error
}

// duplicateWindow keeps Nats-Msg-Id deduplication active for the usual
// publisher retry window.
const duplicateWindow = 2 * time.Minute

// publisherReadyTimeout bounds the broker round trip of the readiness probe.
const publisherReadyTimeout = 2 * time.Second

// NATSPublisher publishes events to a NATS JetStream stream.
type NATSPublisher struct {
	conn   *nats.Conn
	js     nats.JetStreamContext
	stream string
	maxAge time.Duration
}

var _ Publisher = (*NATSPublisher)(nil)

// NewNATSPublisher returns a JetStream publisher over conn. The stream is
// named stream and retains events for retentionDays days.
func NewNATSPublisher(conn *nats.Conn, stream string, retentionDays int) (*NATSPublisher, error) {
	js, err := conn.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}
	return &NATSPublisher{
		conn:   conn,
		js:     js,
		stream: stream,
		maxAge: time.Duration(retentionDays) * 24 * time.Hour,
	}, nil
}

// EnsureStream creates the stream when missing. An existing stream is left
// untouched when its configuration already matches; otherwise it is updated to
// the desired configuration, so repeated calls are idempotent.
func (p *NATSPublisher) EnsureStream(ctx context.Context) error {
	cfg := &nats.StreamConfig{
		Name:       p.stream,
		Subjects:   []string{"wzap.>"},
		Storage:    nats.FileStorage,
		MaxAge:     p.maxAge,
		Duplicates: duplicateWindow,
	}

	if _, err := p.js.AddStream(cfg, nats.Context(ctx)); err != nil {
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return fmt.Errorf("create stream %s: %w", p.stream, err)
		}
		if _, err := p.js.UpdateStream(cfg, nats.Context(ctx)); err != nil {
			return fmt.Errorf("update stream %s: %w", p.stream, err)
		}
	}
	return nil
}

// Publish stores the envelope in the stream, carrying its event id as the
// Nats-Msg-Id header so the broker can drop duplicates.
func (p *NATSPublisher) Publish(ctx context.Context, subject string, env Envelope) error {
	data, err := env.bytes()
	if err != nil {
		return err
	}
	if _, err := p.js.Publish(subject, data, nats.Context(ctx), nats.MsgId(env.EventID.String())); err != nil {
		return fmt.Errorf("publish event %s on %s: %w", env.EventID, subject, err)
	}
	return nil
}

// Ready reports the broker connection health for the readiness probe.
func (p *NATSPublisher) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if status := p.conn.Status(); status != nats.CONNECTED {
		return fmt.Errorf("broker connection is %s", status)
	}
	if err := p.conn.FlushTimeout(publisherReadyTimeout); err != nil {
		return fmt.Errorf("broker flush: %w", err)
	}
	return nil
}
