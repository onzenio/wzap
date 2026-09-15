package whatsmeow

import (
	"context"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// waVersionRefreshTimeout bounds the startup version lookup: pairing must not
// wait on web.whatsapp.com being slow or unreachable.
const waVersionRefreshTimeout = 10 * time.Second

// RefreshWAVersion advertises the latest WhatsApp web client version before
// any session connects. WhatsApp bumps the web client regularly and rejects
// pairings from outdated versions, so the lookup keeps a pinned library
// working until it is updated. A lookup failure only warns and keeps the pin.
func RefreshWAVersion(ctx context.Context, log *slog.Logger) {
	refreshWAVersion(ctx, log, func(ctx context.Context) (*store.WAVersionContainer, error) {
		return whatsmeow.GetLatestVersion(ctx, nil)
	})
}

func refreshWAVersion(ctx context.Context, log *slog.Logger, fetch func(context.Context) (*store.WAVersionContainer, error)) {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, waVersionRefreshTimeout)
	defer cancel()
	latest, err := fetch(ctx)
	if err != nil || latest == nil {
		log.Warn("keeping pinned whatsapp web version", "pinned", store.GetWAVersion().String(), "error", err)
		return
	}
	store.SetWAVersion(*latest)
	log.Info("whatsapp web version refreshed", "version", latest.String())
}
