package chatimport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/chatwoot/client"
	"wzap/internal/model"
	"wzap/internal/session"
)

const opRunImport = "chatimport: run import"

// InboxLister resolves the instance inbox by name. *client.Client implements
// it.
type InboxLister interface {
	ListInboxes(ctx context.Context) ([]client.Inbox, error)
}

// HistoryFeed is the per-instance history-sync feed an import run consumes
// and clears. session.Session implements it.
type HistoryFeed interface {
	HistorySyncSnapshot() session.HistorySyncSnapshot
	ResetHistorySync()
}

// RunDeps wires one import run over an instance feed snapshot: the pool
// (nil stays inert), the connector config (account, token, inbox name and
// import flags), the inbox lookup, the feed, the placeholder switch, an
// extra newer-than cut for the lost-messages window, and the operational
// poster for the pt-BR start/result notices (nil skips notices).
type RunDeps struct {
	Pool        *pgxpool.Pool
	Config      model.ChatwootConfig
	Inboxes     InboxLister
	Feed        HistoryFeed
	Placeholder bool
	NewerThan   time.Time
	Poster      func(ctx context.Context, text string) error
}

// RunImport imports one instance feed snapshot into Chatwoot and returns how
// many messages were written. Contacts import first when flagged, then
// messages; the accumulator is cleared only on success so a failure keeps
// the feed for the next trigger. A nil pool (import disabled) or a config
// with both import flags off is a no-op returning 0, nil.
func RunImport(ctx context.Context, deps RunDeps) (int, error) {
	if deps.Pool == nil {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%s: %w", opRunImport, err)
	}
	if !deps.Config.ImportContacts && !deps.Config.ImportMessages {
		return 0, nil
	}
	if deps.Feed == nil {
		return 0, fmt.Errorf("%s: no history feed", opRunImport)
	}
	inboxID, err := resolveImportInbox(ctx, deps.Inboxes, deps.Config.NameInbox)
	if err != nil {
		return 0, err
	}
	postImportNotice(ctx, deps.Poster, "⏳ Importação do histórico iniciada. Isso pode levar alguns minutos.")
	snap := deps.Feed.HistorySyncSnapshot()
	if deps.Config.ImportContacts {
		if _, err := ImportContacts(ctx, deps.Pool, deps.Config.AccountID, deps.Config.NameInbox, snap.Contacts); err != nil {
			return 0, err
		}
	}
	imported := 0
	if deps.Config.ImportMessages {
		var msgs []session.HistorySyncMessage
		for _, conv := range snap.Conversations {
			msgs = append(msgs, conv.Messages...)
		}
		imported, err = ImportMessages(ctx, MessagesDeps{
			Pool:        deps.Pool,
			AccountID:   deps.Config.AccountID,
			Token:       deps.Config.Token,
			DaysLimit:   deps.Config.DaysLimit,
			NewerThan:   deps.NewerThan,
			Placeholder: deps.Placeholder,
		}, inboxID, msgs)
		if err != nil {
			return 0, err
		}
	}
	deps.Feed.ResetHistorySync()
	postImportNotice(ctx, deps.Poster, fmt.Sprintf("✅ Importação do histórico concluída: %d mensagem(ns) importada(s).", imported))
	return imported, nil
}

// resolveImportInbox maps the configured inbox name to its Chatwoot id. An
// unprovisioned inbox fails the run: importing without a conversation home
// would strand the rows.
func resolveImportInbox(ctx context.Context, inboxes InboxLister, name string) (int64, error) {
	if inboxes == nil {
		return 0, fmt.Errorf("%s: no inbox lookup", opRunImport)
	}
	want := strings.TrimSpace(name)
	if want == "" {
		return 0, fmt.Errorf("%s: no inbox configured", opRunImport)
	}
	list, err := inboxes.ListInboxes(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list inboxes: %w", opRunImport, err)
	}
	for _, inbox := range list {
		if inbox.Name == want {
			return inbox.ID, nil
		}
	}
	return 0, fmt.Errorf("%s: inbox %q not found", opRunImport, want)
}

// postImportNotice delivers one operational notice; notices are best-effort
// and never fail the run.
func postImportNotice(ctx context.Context, poster func(ctx context.Context, text string) error, text string) {
	if poster == nil {
		return
	}
	_ = poster(ctx, text)
}
