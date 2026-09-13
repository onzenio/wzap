package message

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"wzap/internal/session/sessiontest"
)

// fixedRand returns a Humanizer Rand function that always answers value.
func fixedRand(value int) func(int) int {
	return func(int) int { return value }
}

func TestHumanizerPresenceFor(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		msgType      string
		textLen      int
		mediaBytes   int
		firstContact bool
		randValue    int
		want         time.Duration
	}{
		{name: "short text is floored", enabled: true, msgType: TypeText, textLen: 4, want: 1500 * time.Millisecond},
		{name: "longer text grows per character", enabled: true, msgType: TypeText, textLen: 100, want: 4 * time.Second},
		{name: "very long text is capped", enabled: true, msgType: TypeText, textLen: 1000, want: 8 * time.Second},
		{name: "audio grows with size", enabled: true, msgType: "audio", mediaBytes: 1 << 20, want: 2300 * time.Millisecond},
		{name: "audio is capped at 15s", enabled: true, msgType: "audio", mediaBytes: 1 << 30, want: 15 * time.Second},
		{name: "image grows with size", enabled: true, msgType: "image", mediaBytes: 3 << 20, want: 2900 * time.Millisecond},
		{name: "media is capped at 8s", enabled: true, msgType: "video", mediaBytes: 1 << 30, want: 8 * time.Second},
		{name: "document uses the media rule", enabled: true, msgType: "document", want: 2 * time.Second},
		{name: "other types fall back to the default", enabled: true, msgType: TypeLocation, want: 1500 * time.Millisecond},
		{name: "first contact adds the study bump", enabled: true, msgType: TypeText, textLen: 100, firstContact: true, want: 5500 * time.Millisecond},
		{name: "first contact bump is jittered", enabled: true, msgType: TypeText, textLen: 100, firstContact: true, randValue: 999, want: 6499 * time.Millisecond},
		{name: "disabled introduces no delay", enabled: false, msgType: TypeText, textLen: 1000, firstContact: true, randValue: 999, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			humanizer := Humanizer{Enabled: tt.enabled, Rand: fixedRand(tt.randValue)}

			got := humanizer.PresenceFor(tt.msgType, tt.textLen, tt.mediaBytes, tt.firstContact)

			if got != tt.want {
				t.Errorf("PresenceFor(%q, %d, %d, %t) = %s, want %s",
					tt.msgType, tt.textLen, tt.mediaBytes, tt.firstContact, got, tt.want)
			}
		})
	}
}

func TestHumanizerPresenceForDefaultRandStaysInRange(t *testing.T) {
	humanizer := Humanizer{Enabled: true}

	got := humanizer.PresenceFor(TypeText, 10, 0, true)

	if got < 3*time.Second || got >= 4*time.Second {
		t.Errorf("PresenceFor with the default Rand = %s, want the text floor plus 1.5-2.5s", got)
	}
}

func TestHumanizerBeforeSend(t *testing.T) {
	errPresence := errors.New("presence failed")

	tests := []struct {
		name            string
		humanizer       Humanizer
		delay           time.Duration
		sendPresenceErr error
		sleepErr        error
		wantPresence    []string
		wantSleeps      []time.Duration
		wantErr         bool
	}{
		{
			name: "enabled signals composing then paused", humanizer: Humanizer{Enabled: true},
			delay: 2 * time.Second, wantPresence: []string{"composing", "paused"},
			wantSleeps: []time.Duration{2 * time.Second},
		},
		{
			name: "disabled does nothing", humanizer: Humanizer{}, delay: 2 * time.Second,
		},
		{
			name: "zero delay does nothing", humanizer: Humanizer{Enabled: true}, delay: 0,
		},
		{
			name: "composing failure stops before the sleep", humanizer: Humanizer{Enabled: true},
			delay: 2 * time.Second, sendPresenceErr: errPresence,
			wantPresence: []string{"composing"}, wantErr: true,
		},
		{
			name: "sleep failure stops before paused", humanizer: Humanizer{Enabled: true},
			delay: 2 * time.Second, sleepErr: context.Canceled,
			wantPresence: []string{"composing"}, wantSleeps: []time.Duration{2 * time.Second}, wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := sessiontest.NewSession(uuid.New(), nil)
			sess.SendPresenceErr = tt.sendPresenceErr

			var sleeps []time.Duration
			tt.humanizer.Sleep = func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return tt.sleepErr
			}

			err := tt.humanizer.BeforeSend(context.Background(), sess, "5547988359190@s.whatsapp.net", tt.delay)

			if tt.wantErr && err == nil {
				t.Error("BeforeSend error = nil, want a failure")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("BeforeSend error = %v, want nil", err)
			}
			if got, want := presenceStates(sess), tt.wantPresence; !slices.Equal(got, want) {
				t.Errorf("presence states = %v, want %v", got, want)
			}
			if got, want := sleeps, tt.wantSleeps; !slices.Equal(got, want) {
				t.Errorf("sleeps = %v, want %v", got, want)
			}
			for i, call := range sess.PresenceCalls() {
				if call.ChatJID != "5547988359190@s.whatsapp.net" {
					t.Errorf("presence[%d] chat = %q, want the target chat", i, call.ChatJID)
				}
			}
		})
	}
}

func TestHumanizerBeforeSendPausedFailureReturnsError(t *testing.T) {
	errPaused := errors.New("paused failed")
	sess := sessiontest.NewSession(uuid.New(), nil)

	humanizer := Humanizer{Enabled: true, Sleep: func(context.Context, time.Duration) error {
		sess.SendPresenceErr = errPaused
		return nil
	}}

	err := humanizer.BeforeSend(context.Background(), sess, "5547988359190@s.whatsapp.net", time.Second)

	if !errors.Is(err, errPaused) {
		t.Errorf("BeforeSend error = %v, want the paused failure", err)
	}
	if got := presenceStates(sess); len(got) != 2 || got[1] != "paused" {
		t.Errorf("presence states = %v, want composing then paused", got)
	}
}

func TestHumanizerBeforeSendDefaultSleepRespectsCanceledContext(t *testing.T) {
	sess := sessiontest.NewSession(uuid.New(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Humanizer{Enabled: true}.BeforeSend(ctx, sess, "5547988359190@s.whatsapp.net", time.Minute)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("BeforeSend error = %v, want context.Canceled", err)
	}
	if got := presenceStates(sess); len(got) != 1 || got[0] != "composing" {
		t.Errorf("presence states = %v, want only composing", got)
	}
}

func TestContentTextLen(t *testing.T) {
	tests := []struct {
		name    string
		msgType string
		payload string
		want    int
	}{
		{name: "text counts its length", msgType: TypeText, payload: `{"text":"olá"}`, want: 4},
		{name: "empty text is zero", msgType: TypeText, payload: `{"text":""}`, want: 0},
		{name: "other types have no typing time", msgType: TypeLocation, payload: `{"latitude":1}`, want: 0},
		{name: "malformed payload is zero", msgType: TypeText, payload: `not json`, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentTextLen(tt.msgType, []byte(tt.payload)); got != tt.want {
				t.Errorf("contentTextLen(%q, %q) = %d, want %d", tt.msgType, tt.payload, got, tt.want)
			}
		})
	}
}

func presenceStates(sess *sessiontest.FakeSession) []string {
	calls := sess.PresenceCalls()
	states := make([]string, len(calls))
	for i, call := range calls {
		states[i] = call.State
	}
	return states
}
