package message

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"wzap/internal/session"
)

// Presence states signalled before an outbound send. They match the chat
// presence strings accepted by session.Session.SendPresence.
const (
	presenceComposing = "composing"
	presencePaused    = "paused"
)

// Humanization timing ported from the apime study (MIT, see
// THIRD_PARTY_NOTICES.md): typing time grows with the content, recording time
// is capped and a first contact looks slower.
const (
	textCharDelay      = 40 * time.Millisecond
	textMinDelay       = 1500 * time.Millisecond
	textMaxDelay       = 8 * time.Second
	mediaBaseDelay     = 2 * time.Second
	mediaPerMBDelay    = 300 * time.Millisecond
	mediaMaxDelay      = 8 * time.Second
	audioMaxDelay      = 15 * time.Second
	defaultDelay       = 1500 * time.Millisecond
	firstContactDelay  = 1500 * time.Millisecond
	firstContactJitter = 1000 // milliseconds
)

// Message types the humanizer knows beyond the stored ones. Media is stored as
// TypeMedia; the finer names let the study's recording cap apply to a voice
// note once a caller can tell it apart.
const (
	typeAudio    = "audio"
	typeImage    = "image"
	typeVideo    = "video"
	typeDocument = "document"
)

// Humanizer simulates a human typing or recording presence before an outbound
// send. When Enabled is false it introduces no delay at all. Sleep and Rand are
// injectable for tests; their zero values fall back to a context-aware sleep
// and the global pseudo-random source.
type Humanizer struct {
	Enabled bool
	Sleep   func(ctx context.Context, d time.Duration) error
	Rand    func(n int) int
}

// PresenceFor returns how long a message of msgType should look like it is
// being written: text grows at ~40ms per character between 1.5s and 8s, media
// grows with the file size up to 8s, a voice note is capped at 15s and an
// unknown type falls back to 1.5s. A first contact adds 1.5-2.5s. A disabled
// humanizer returns 0.
func (h Humanizer) PresenceFor(msgType string, textLen, mediaBytes int, firstContact bool) time.Duration {
	if !h.Enabled {
		return 0
	}

	var delay time.Duration
	switch msgType {
	case TypeText:
		delay = capDelay(time.Duration(textLen)*textCharDelay, textMaxDelay)
		if delay < textMinDelay {
			delay = textMinDelay
		}
	case typeAudio:
		delay = capDelay(mediaBaseDelay+sizeDelay(mediaBytes), audioMaxDelay)
	case TypeMedia, typeImage, typeVideo, typeDocument:
		delay = capDelay(mediaBaseDelay+sizeDelay(mediaBytes), mediaMaxDelay)
	default:
		delay = defaultDelay
	}

	if firstContact {
		delay += firstContactDelay + time.Duration(h.rand(firstContactJitter))*time.Millisecond
	}
	return delay
}

// BeforeSend signals composing presence, waits d and signals paused, so the
// message that follows is not delivered faster than the simulated writing. A
// disabled humanizer or a non-positive delay touches neither the session nor
// the clock.
func (h Humanizer) BeforeSend(ctx context.Context, sess session.Session, chatJID string, d time.Duration) error {
	if !h.Enabled || d <= 0 {
		return nil
	}
	if err := sess.SendPresence(ctx, chatJID, presenceComposing); err != nil {
		return fmt.Errorf("send composing presence: %w", err)
	}
	if err := h.sleep(ctx, d); err != nil {
		return err
	}
	if err := sess.SendPresence(ctx, chatJID, presencePaused); err != nil {
		return fmt.Errorf("send paused presence: %w", err)
	}
	return nil
}

// sleep waits for d through the injected function, or a context-aware sleep
// when none was configured.
func (h Humanizer) sleep(ctx context.Context, d time.Duration) error {
	if h.Sleep != nil {
		return h.Sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

// rand draws from the injected source, or the global one when none was
// configured.
func (h Humanizer) rand(n int) int {
	if h.Rand != nil {
		return h.Rand(n)
	}
	return rand.Intn(n)
}

// sizeDelay grows the media delay by whole megabytes, as in the study.
func sizeDelay(mediaBytes int) time.Duration {
	if mediaBytes <= 0 {
		return 0
	}
	return time.Duration(mediaBytes/(1<<20)) * mediaPerMBDelay
}

func capDelay(d, limit time.Duration) time.Duration {
	if d > limit {
		return limit
	}
	return d
}
