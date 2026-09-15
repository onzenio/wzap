package whatsmeow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRefreshWAVersionAdvertisesLatest verifies the startup hardening: a
// successful lookup advertises the fetched WhatsApp web version.
func TestRefreshWAVersionAdvertisesLatest(t *testing.T) {
	prev := store.GetWAVersion()
	defer store.SetWAVersion(prev)

	want := &store.WAVersionContainer{2, 3000, 1047451014}
	refreshWAVersion(context.Background(), discardLogger(), func(context.Context) (*store.WAVersionContainer, error) {
		return want, nil
	})
	if got := store.GetWAVersion(); got != *want {
		t.Fatalf("WA version = %v, want %v", got, *want)
	}
}

// TestRefreshWAVersionKeepsPinOnFailure verifies the lookup is non-fatal: a
// failed fetch keeps the pinned version instead of breaking the boot.
func TestRefreshWAVersionKeepsPinOnFailure(t *testing.T) {
	prev := store.GetWAVersion()
	defer store.SetWAVersion(prev)

	refreshWAVersion(context.Background(), discardLogger(), func(context.Context) (*store.WAVersionContainer, error) {
		return nil, errors.New("web.whatsapp.com unreachable")
	})
	if got := store.GetWAVersion(); got != prev {
		t.Fatalf("WA version = %v, want pinned %v", got, prev)
	}
}
