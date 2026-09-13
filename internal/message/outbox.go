package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"wzap/internal/events"
	"wzap/internal/instancelock"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/storage"
)

const (
	// defaultOutboxBatchSize is how many due messages a worker claims at once.
	defaultOutboxBatchSize = 10
	// defaultOutboxPollInterval is how long an idle worker waits before
	// claiming again.
	defaultOutboxPollInterval = time.Second
	// defaultOutboxRecoveryInterval is how often stuck messages are requeued.
	defaultOutboxRecoveryInterval = 30 * time.Second
	// stuckThreshold is how long a message may stay in sending before the
	// recovery requeues it.
	stuckThreshold = 5 * time.Minute
	// maxSendRetries is how many transient failures a message survives before
	// it is failed definitively.
	maxSendRetries = 5
	// maxRetryDelay caps the exponential retry backoff.
	maxRetryDelay = 2 * time.Minute
	// messageStatusEventType is the event type of the outbound status events.
	messageStatusEventType = "message.status"
)

// OutboxStore is the message queue persistence consumed by the outbox workers.
type OutboxStore interface {
	// ClaimQueued atomically moves due queued messages to sending.
	ClaimQueued(ctx context.Context, limit int) ([]model.OutboundMessage, error)
	MarkSent(ctx context.Context, id uuid.UUID, whatsAppMessageID string) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
	MarkRetrying(ctx context.Context, id uuid.UUID, errMsg string, nextAttemptAt time.Time) error
	// RequeueStuck moves sending messages updated before olderThan to queued.
	RequeueStuck(ctx context.Context, olderThan time.Time) (int64, error)
}

// The concrete repository satisfies the outbox contract; the interface
// assertion catches signature drift at build time.
var _ OutboxStore = (storage.MessageRepository)(nil)

// Outbox delivers the queued messages of every instance with a pool of
// workers. Messages of the same instance are serialized by the per-instance
// lock; different instances are delivered in parallel.
type Outbox struct {
	repo      OutboxStore
	manager   session.Manager
	writer    events.Writer
	log       *slog.Logger
	workers   int
	lock      *instancelock.Locker
	senders   map[string]Sender
	humanizer Humanizer

	batchSize        int
	pollInterval     time.Duration
	recoveryInterval time.Duration
	now              func() time.Time
	sleep            func(ctx context.Context, d time.Duration) error
}

// NewOutbox builds the outbox over its dependencies. A nil logger falls back
// to the default one, a nil locker to a fresh one and a non-positive worker
// count to a single worker. Humanization stays off unless humanize is true; a
// nil media resolver leaves media messages unsupported.
func NewOutbox(
	repo OutboxStore,
	manager session.Manager,
	writer events.Writer,
	media MediaPathResolver,
	log *slog.Logger,
	workers int,
	lock *instancelock.Locker,
	humanize bool,
) *Outbox {
	if log == nil {
		log = slog.Default()
	}
	if workers <= 0 {
		workers = 1
	}
	if lock == nil {
		lock = instancelock.New()
	}
	return &Outbox{
		repo:             repo,
		manager:          manager,
		writer:           writer,
		log:              log,
		workers:          workers,
		lock:             lock,
		senders:          defaultSenders(media),
		humanizer:        Humanizer{Enabled: humanize},
		batchSize:        defaultOutboxBatchSize,
		pollInterval:     defaultOutboxPollInterval,
		recoveryInterval: defaultOutboxRecoveryInterval,
		now:              time.Now,
		sleep:            sleepContext,
	}
}

// Run recovers the messages stuck in sending, then delivers claimed messages
// with workers goroutines until ctx is canceled. It returns after every worker
// stopped; a message being sent when the context is canceled stays in sending
// and is requeued by the recovery once it is older than stuckThreshold.
func (o *Outbox) Run(ctx context.Context) {
	o.StartRecovery(ctx)

	var wg sync.WaitGroup
	for i := 0; i < o.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.worker(ctx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.recoveryLoop(ctx)
	}()

	<-ctx.Done()
	wg.Wait()
}

// StartRecovery requeues the messages stuck in sending past stuckThreshold.
func (o *Outbox) StartRecovery(ctx context.Context) {
	recovered, err := o.repo.RequeueStuck(ctx, o.now().Add(-stuckThreshold))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		o.log.WarnContext(ctx, "requeue stuck messages", "error", err)
		return
	}
	if recovered > 0 {
		o.log.InfoContext(ctx, "requeued stuck messages", "count", recovered)
	}
}

// recoveryLoop requeues stuck messages periodically, so a worker that died
// mid-send does not leave the message stuck until the next restart.
func (o *Outbox) recoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(o.recoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.StartRecovery(ctx)
		}
	}
}

// worker claims and delivers batches until ctx is canceled.
func (o *Outbox) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		claimed, err := o.repo.ClaimQueued(ctx, o.batchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			o.log.WarnContext(ctx, "claim queued messages", "error", err)
			if o.wait(ctx, o.pollInterval) != nil {
				return
			}
			continue
		}

		for i := range claimed {
			if ctx.Err() != nil {
				return
			}
			o.process(ctx, claimed[i])
		}

		if len(claimed) < o.batchSize {
			if o.wait(ctx, o.pollInterval) != nil {
				return
			}
		}
	}
}

// process delivers one claimed message and records its outcome.
func (o *Outbox) process(ctx context.Context, msg model.OutboundMessage) {
	release, err := o.lock.Acquire(ctx, msg.InstanceID)
	if err != nil {
		// Shutdown while waiting for the instance: leave the message in
		// sending for the recovery to requeue.
		return
	}
	defer release()

	sess, ok := o.manager.Get(msg.InstanceID)
	if !ok {
		o.fail(ctx, msg, "session not found for instance "+msg.InstanceID.String())
		return
	}

	sender, ok := o.senders[msg.Type]
	if !ok {
		o.fail(ctx, msg, fmt.Sprintf("unsupported message type %q", msg.Type))
		return
	}

	if err := o.simulatePresence(ctx, sess, msg); err != nil {
		o.handleSendError(ctx, msg, err)
		return
	}

	whatsappID, err := sender.Send(ctx, sess, msg)
	if err != nil {
		o.handleSendError(ctx, msg, err)
		return
	}

	o.complete(ctx, msg, whatsappID)
}

// simulatePresence types before the send when humanization is enabled. Presence
// is best effort: a typing-indicator hiccup must not retry or fail a message
// that was never sent, so it is logged and the send proceeds. A canceled
// context still aborts, leaving the message in sending for the recovery. v1
// keeps no inbound history, so a send cannot be told apart from an open
// conversation; the delay always uses the open-conversation profile, and first
// contact stays available for when that signal exists. The media size arrives
// with the media pipeline, so media messages currently use the base delay only.
func (o *Outbox) simulatePresence(ctx context.Context, sess session.Session, msg model.OutboundMessage) error {
	delay := o.humanizer.PresenceFor(msg.Type, contentTextLen(msg.Type, msg.Payload), 0, false)
	if err := o.humanizer.BeforeSend(ctx, sess, msg.RecipientJID, delay); err != nil {
		if ctx.Err() != nil {
			// Shutdown or timeout: leave the message in sending for the
			// recovery instead of sending it on a dead context.
			return err
		}
		o.log.WarnContext(ctx, "simulate send presence", "message_id", msg.ID, "error", err)
	}
	return nil
}

// contentTextLen returns the typing budget of a stored payload: the length of a
// text message, zero for the other types.
func contentTextLen(msgType string, payload []byte) int {
	if msgType != TypeText {
		return 0
	}
	var body textPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0
	}
	return len(body.Text)
}

// handleSendError retries a transient failure with exponential backoff while
// fewer than maxSendRetries retries happened, and fails the message
// definitively otherwise. The backoff is computed from the attempts before
// this call because MarkRetrying increments the stored counter itself.
func (o *Outbox) handleSendError(ctx context.Context, msg model.OutboundMessage, cause error) {
	if ctx.Err() != nil {
		// Shutdown: leave the message in sending for the recovery.
		return
	}

	if errors.Is(cause, session.ErrTransient) && msg.Attempts < maxSendRetries {
		nextAttemptAt := o.now().Add(retryDelay(msg.Attempts))
		if err := o.repo.MarkRetrying(ctx, msg.ID, cause.Error(), nextAttemptAt); err != nil {
			o.log.ErrorContext(ctx, "schedule message retry", "message_id", msg.ID, "error", err)
		}
		return
	}

	o.fail(ctx, msg, cause.Error())
}

// complete records a delivered message and enqueues its status event.
func (o *Outbox) complete(ctx context.Context, msg model.OutboundMessage, whatsappID string) {
	if err := o.repo.MarkSent(ctx, msg.ID, whatsappID); err != nil {
		o.log.ErrorContext(ctx, "mark message sent", "message_id", msg.ID, "error", err)
		return
	}
	o.emit(ctx, msg, StatusSent, whatsappID, "")
}

// fail records a definitive failure and enqueues its status event.
func (o *Outbox) fail(ctx context.Context, msg model.OutboundMessage, errMsg string) {
	if err := o.repo.MarkFailed(ctx, msg.ID, errMsg); err != nil {
		o.log.ErrorContext(ctx, "mark message failed", "message_id", msg.ID, "error", err)
		return
	}
	o.emit(ctx, msg, StatusFailed, "", errMsg)
}

// emit enqueues the message.status event. A write failure is logged and
// dropped: the message state is already persisted and the wakeup is best
// effort.
func (o *Outbox) emit(ctx context.Context, msg model.OutboundMessage, status, whatsappID, errMsg string) {
	env, err := events.New(messageStatusEventType, msg.InstanceID, messageStatusPayload{
		MessageID:  msg.ID,
		Status:     status,
		WhatsAppID: whatsappID,
		Error:      errMsg,
	})
	if err != nil {
		o.log.ErrorContext(ctx, "build message status event", "message_id", msg.ID, "error", err)
		return
	}
	if err := o.writer.Write(ctx, events.Subjects.MessageStatus(msg.InstanceID), env); err != nil {
		o.log.WarnContext(ctx, "enqueue message status event", "message_id", msg.ID, "error", err)
	}
}

// messageStatusPayload is the JSON body of a message.status event. WhatsAppID
// is set on sent and Error on failed.
type messageStatusPayload struct {
	MessageID  uuid.UUID `json:"message_id"`
	Status     string    `json:"status"`
	WhatsAppID string    `json:"whatsapp_id,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// retryDelay returns the delay before the attempt that follows retries
// transient failures: 2^retries seconds, capped at maxRetryDelay.
func retryDelay(retries int) time.Duration {
	delay := time.Second
	for i := 0; i < retries; i++ {
		delay *= 2
		if delay >= maxRetryDelay {
			return maxRetryDelay
		}
	}
	return delay
}

// wait sleeps for d, returning the context error when canceled first.
func (o *Outbox) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	return o.sleep(ctx, d)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
