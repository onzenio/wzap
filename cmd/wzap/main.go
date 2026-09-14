// Command wzap runs the WhatsApp bridge service.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"wzap/internal/app"
	"wzap/internal/chatwoot/client"
	"wzap/internal/chatwoot/contacts"
	"wzap/internal/chatwoot/conversations"
	"wzap/internal/chatwoot/import"
	"wzap/internal/chatwoot/inbound"
	"wzap/internal/chatwoot/mirror"
	"wzap/internal/config"
	"wzap/internal/events"
	"wzap/internal/httpapi"
	"wzap/internal/instance"
	"wzap/internal/instancelock"
	"wzap/internal/media"
	"wzap/internal/message"
	"wzap/internal/model"
	"wzap/internal/session"
	"wzap/internal/session/whatsmeow"
	"wzap/internal/storage"
	"wzap/internal/storage/postgres"
	"wzap/internal/version"
	"wzap/internal/webhook"
)

const (
	shutdownTimeout     = 10 * time.Second
	healthcheckTimeout  = 5 * time.Second
	natsConnectTimeout  = 5 * time.Second
	streamEnsureTimeout = 5 * time.Second
	restoreTimeout      = 30 * time.Second
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wzap:", err)
		os.Exit(1)
	}
}

// run dispatches the subcommands; serving is the default.
func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		return serve()
	case "migrate":
		return migrate()
	case "healthcheck":
		return healthcheck()
	default:
		return fmt.Errorf("unknown command %q, want serve, migrate or healthcheck", command)
	}
}

// serve runs the HTTP server until an interrupt or termination signal. The
// shutdown drains the in-flight requests first, then stops the outbox, the
// media cleaner, the webhook worker, the chatwoot mirror and the chatwoot
// import scheduler, and finally the relay, all within shutdownTimeout.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log, err := newLogger(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	// The Chatwoot history-import pool is a process singleton; serve owns its
	// single Close at shutdown.
	defer chatimport.Close()

	if cfg.AutoMigrate {
		if err := postgres.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}

	nc, err := nats.Connect(cfg.NATSURL,
		nats.Name("wzap"),
		nats.Timeout(natsConnectTimeout),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
	)
	if err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer nc.Close()

	publisher, err := events.NewNATSPublisher(nc, cfg.NATSStream, cfg.EventRetentionDays)
	if err != nil {
		return fmt.Errorf("event publisher: %w", err)
	}

	ensureCtx, cancelEnsure := context.WithTimeout(ctx, streamEnsureTimeout)
	ensureErr := publisher.EnsureStream(ensureCtx)
	cancelEnsure()
	switch {
	case ensureErr != nil && nc.IsConnected():
		return fmt.Errorf("ensure event stream: %w", ensureErr)
	case ensureErr != nil:
		// The broker is down at boot: serve anyway, report it through /readyz
		// and let the relay ensure the stream once the broker returns.
		log.Warn("event stream not ready at startup", "error", ensureErr)
	}

	instances := postgres.NewInstanceRepository(pool)
	users := postgres.NewUserRepository(pool)
	keys := postgres.NewAPIKeyRepository(pool)

	// Seed the initial admin (and claim the legacy ownerless instances for
	// him) before serving. Without WZAP_ADMIN_* this is a no-op.
	if err := seedAdmin(ctx, cfg, users, instances, log); err != nil {
		return err
	}

	messageRepo := postgres.NewMessageRepository(pool)
	idempotencyRepo := postgres.NewIdempotencyRepository(pool)
	outbox := postgres.NewEventOutboxRepository(pool)
	mediaStorage := media.NewStorage(cfg.DataDir, postgres.NewMediaRepository(pool),
		cfg.MaxMediaBytes, time.Duration(cfg.MediaTTLSeconds)*time.Second)
	relay := events.NewRelay(outbox, publisher, log, cfg.EventRetentionDays)
	checker := httpapi.NewChecker(pool, httpapi.NamedProbe{Name: "nats", Run: publisher.Ready})

	eventWriter := events.NewWriter(outbox)
	// Webhook fan-out: every event of every type flows through Writer
	// (message, receipt, connection, message.status), so decorating it once
	// hooks all producers with no per-producer wiring. The NATS relay replays
	// from the DB outbox, NOT through Writer, so there is no double delivery.
	webhookWorker := webhook.NewWorker(instances, webhook.Keys, webhook.Deliver, cfg.MaxMediaBytes, log)
	webhookWriter := webhookWorker.Fanout(eventWriter)

	runtime := app.NewRuntime(
		instances, webhookWriter, message.NewReceipts(messageRepo, webhookWriter, cfg.MaxMediaBytes),
		mediaStorage, cfg.PublicURL, cfg.MaxMediaBytes, log,
	)
	sessions, err := whatsmeow.NewManager(ctx, cfg.DatabaseURL, instances, log, runtime, cfg.MaxMediaBytes)
	if err != nil {
		return fmt.Errorf("session manager: %w", err)
	}
	defer func() { _ = sessions.Close() }()

	// Chatwoot mirror: a READ-only JetStream consumer (durable
	// "wzap-chatwoot") that reflects inbound events into Chatwoot. It is
	// only wired when the connector is globally enabled; per-instance
	// switches are enforced by the worker itself. The consumer never
	// publishes, so there is no second relay. It is built after the session
	// manager so pairing notices can fetch the live QR string at handle
	// time through the session's in-memory pairing state.
	//
	// The config/correlation repositories are always wired (cheap pgx
	// wrappers): the REST set/find and the open webhook need them to answer
	// the global 400 gate and the disabled-with-empty-fields reads even
	// when the mirror consumer stays down.
	chatwootConfigs, chatwootMessages := postgres.NewChatwootRepositories(pool)
	var mirrorWorker *mirror.Worker
	// The history importer backs the manual REST trigger, the post-pairing
	// auto import and the lost-messages cron. Without the import URI every
	// run stays inert (0, nil) and the rest of the connector keeps working.
	importer := &chatwootHistoryImporter{
		configs:  chatwootConfigs,
		sessions: sessions,
		clientFor: func(connector model.ChatwootConfig) chatimport.InboxLister {
			return client.New(connector.URL, connector.Token, connector.AccountID)
		},
		notify: func(ctx context.Context, instanceID uuid.UUID, text string) error {
			if mirrorWorker == nil {
				return nil
			}
			return mirrorWorker.NotifyOperational(ctx, instanceID, text)
		},
		placeholder: cfg.Chatwoot.ImportPlaceholder,
	}
	if cfg.Chatwoot.Enabled {
		clientFor := func(connector model.ChatwootConfig) mirror.ChatwootClient {
			return client.New(connector.URL, connector.Token, connector.AccountID)
		}
		// Boot-fail-closed: the worker abstracts the client behind an
		// interface, but the resolvers need the concrete client. Validate the
		// wiring once so a programming mistake fails boot with a clear error
		// instead of panicking on the first event.
		if _, ok := clientFor(model.ChatwootConfig{}).(*client.Client); !ok {
			return fmt.Errorf("chatwoot mirror miswired: ClientFor must return *client.Client")
		}
		contactsFor := func(cli mirror.ChatwootClient, connector model.ChatwootConfig) mirror.ContactResolver {
			concrete, ok := cli.(*client.Client)
			if !ok {
				return miswiredContactResolver{err: fmt.Errorf("chatwoot mirror miswired: contacts need *client.Client, got %T", cli)}
			}
			return contacts.New(concrete, connector, log)
		}
		conversationsFor := func(cli mirror.ChatwootClient, connector model.ChatwootConfig, inboxID int64) mirror.ConversationResolver {
			concrete, ok := cli.(*client.Client)
			if !ok {
				return miswiredConversationResolver{err: fmt.Errorf("chatwoot mirror miswired: conversations need *client.Client, got %T", cli)}
			}
			return conversations.New(concrete, connector, inboxID, log)
		}
		mirrorWorker = mirror.New(mirror.Deps{
			Conn:             nc,
			Stream:           cfg.NATSStream,
			Configs:          chatwootConfigs,
			Messages:         chatwootMessages,
			Media:            mediaStorage,
			QR:               sessionQRProvider{sessions: sessions},
			Global:           cfg.Chatwoot,
			ClientFor:        clientFor,
			ContactsFor:      contactsFor,
			ConversationsFor: conversationsFor,
			ImportTrigger: func(ctx context.Context, instanceID uuid.UUID) error {
				_, err := importer.run(ctx, instanceID, time.Time{})
				return err
			},
			Log: log,
		})
	}

	// Chatwoot lost-messages cron: re-imports the recent history every 30
	// minutes, covering what the live mirror missed. It stays down with the
	// connector off and inert without the import URI.
	var importScheduler *chatimport.Scheduler
	if cfg.Chatwoot.Enabled {
		importScheduler = chatimport.NewScheduler(chatimport.SchedulerDeps{
			Instances: instances,
			Configs:   chatwootConfigs,
			Sessions:  sessions,
			Run: func(ctx context.Context, instanceID uuid.UUID, since time.Time) (int, error) {
				return importer.run(ctx, instanceID, since)
			},
			ClearCache: func(instanceID uuid.UUID) {
				if mirrorWorker != nil {
					mirrorWorker.Clear(instanceID)
				}
			},
			Log: log,
		})
	}

	service := instance.NewService(instances, sessions, mediaStorage, users, keys)
	numbers := message.NewJIDResolver(sessions, postgres.NewJIDCacheRepository(pool), log)
	messages := message.NewService(instances, numbers, messageRepo)

	// Chatwoot inbound (capability wzap-chatwoot-inbound): the open webhook
	// reuses message.Service.Enqueue (text/media via media.Storage after
	// downloading data_url; quoted via correlation; private-note on
	// failure), the session fakes surface (DeleteMessage/MarkRead/PairPhone
	// for reverse-delete/template/mark-read and init:<number>), and the
	// per-instance Chatwoot API for notes and operational confirmations. It
	// has no background worker: handling is synchronous in the webhook
	// route, so the shutdown order (HTTP drain, then outbox, cleaner,
	// webhook worker, chatwoot mirror, import scheduler, relay last) is
	// preserved with the mirror and the scheduler stopping before the relay.
	var chatwootCache inbound.CacheClearer
	if mirrorWorker != nil {
		chatwootCache = mirrorWorker
	}
	inboundHandler := inbound.New(inbound.Deps{
		Configs:      chatwootConfigs,
		Correlations: chatwootMessages,
		Instances:    service,
		Enqueuer:     messages,
		Media:        mediaStorage,
		Sessions:     sessions,
		Downloader:   inbound.NewHTTPDownloader(cfg.MaxMediaBytes),
		Cache:        chatwootCache,
		Global:       cfg.Chatwoot,
		Log:          log,
		ClientFor: func(connector model.ChatwootConfig) inbound.ChatwootAPI {
			return client.New(connector.URL, connector.Token, connector.AccountID)
		},
	})

	// Restore the persisted sessions before serving and before the outbox
	// starts claiming messages. Per-instance failures are reflected in
	// instances.status by the manager; only an aborted restore is reported
	// here, and serving proceeds either way.
	restoreCtx, cancelRestore := context.WithTimeout(ctx, restoreTimeout)
	restoreErr := service.Restore(restoreCtx)
	cancelRestore()
	if restoreErr != nil {
		log.Warn("restore sessions not completed", "error", restoreErr)
	}

	outboxWorker := message.NewOutbox(messageRepo, sessions, webhookWriter, mediaStorage, log, cfg.OutboxWorkers, instancelock.New(), cfg.Humanize)

	srv := httpapi.New(cfg, log, httpapi.Deps{
		ReadyChecker:     checker,
		Instances:        service,
		Numbers:          numbers,
		Messages:         messages,
		Idempotency:      idempotencyRepo,
		Media:            mediaStorage,
		Users:            users,
		Keys:             keys,
		JWTSecret:        cfg.JWTSecret,
		ChatwootConfigs:  chatwootConfigs,
		Chatwoot:         cfg.Chatwoot,
		PublicURL:        cfg.PublicURL,
		ChatwootInbound:  inboundHandler,
		ChatwootImporter: importer,
		ChatwootClientFor: func(connector model.ChatwootConfig) httpapi.ChatwootInboxClient {
			return client.New(connector.URL, connector.Token, connector.AccountID)
		},
	})

	// The workers do not derive from the signal context: SIGTERM must not stop
	// them behind the shutdown sequence. Each is cancelled explicitly below,
	// in the order the shutdown comment describes.
	outboxCtx, stopOutbox := context.WithCancel(context.Background())
	defer stopOutbox()
	outboxDone := make(chan struct{})
	go func() {
		defer close(outboxDone)
		outboxWorker.Run(outboxCtx)
	}()

	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.Run(relayCtx)
	}()

	cleaner := media.NewCleaner(mediaStorage, log)
	cleanerCtx, stopCleaner := context.WithCancel(context.Background())
	defer stopCleaner()
	cleanerDone := make(chan struct{})
	go func() {
		defer close(cleanerDone)
		cleaner.Run(cleanerCtx)
	}()

	webhookCtx, stopWebhook := context.WithCancel(context.Background())
	defer stopWebhook()
	webhookDone := make(chan struct{})
	go func() {
		defer close(webhookDone)
		webhookWorker.Run(webhookCtx)
	}()

	mirrorCtx, stopMirror := context.WithCancel(context.Background())
	defer stopMirror()
	mirrorDone := make(chan struct{})
	go func() {
		defer close(mirrorDone)
		if mirrorWorker != nil {
			mirrorWorker.Run(mirrorCtx)
		}
	}()

	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	defer stopScheduler()
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		if importScheduler != nil {
			importScheduler.Start(schedulerCtx)
		}
	}()

	log.Info("wzap listening", "version", version.Version, "addr", cfg.HTTPAddr)

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		stopComponents(shutdownCtx, log,
			shutdownComponent{name: "outbox", stop: stopOutbox, done: outboxDone},
			shutdownComponent{name: "media cleaner", stop: stopCleaner, done: cleanerDone},
			shutdownComponent{name: "webhook worker", stop: stopWebhook, done: webhookDone},
			shutdownComponent{name: "chatwoot mirror", stop: stopMirror, done: mirrorDone},
			shutdownComponent{name: "chatwoot import scheduler", stop: stopScheduler, done: schedulerDone},
			shutdownComponent{name: "event relay", stop: stopRelay, done: relayDone},
		)
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	stop() // a second signal aborts the drain instead of being ignored

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)

	// The HTTP server drains first so in-flight requests finish and can still
	// enqueue events. Only then the outbox stops sending, the cleaner stops
	// deleting media, the webhook worker stops (dropping its pending queue
	// without a drain: webhooks are best-effort and the shared budget belongs
	// to the relay), the chatwoot mirror and the import scheduler stop (no
	// new import starts while draining), and the relay stops last, after
	// publishing the events those requests and the outbox enqueued. Every
	// wait shares the shutdown deadline, so the whole sequence stays bounded.
	stopComponents(shutdownCtx, log,
		shutdownComponent{name: "outbox", stop: stopOutbox, done: outboxDone},
		shutdownComponent{name: "media cleaner", stop: stopCleaner, done: cleanerDone},
		shutdownComponent{name: "webhook worker", stop: stopWebhook, done: webhookDone},
		shutdownComponent{name: "chatwoot mirror", stop: stopMirror, done: mirrorDone},
		shutdownComponent{name: "chatwoot import scheduler", stop: stopScheduler, done: schedulerDone},
		shutdownComponent{name: "event relay", stop: stopRelay, done: relayDone},
	)

	if shutdownErr != nil {
		return fmt.Errorf("shutdown http server: %w", shutdownErr)
	}
	log.Info("wzap stopped")
	return nil
}

// shutdownComponent pairs a background worker with its cancel function and the
// channel closed when its Run returns.
type shutdownComponent struct {
	name string
	stop context.CancelFunc
	done <-chan struct{}
}

// chatwootHistoryImporter backs the history import triggers: the manual
// POST /instances/{id}/chatwoot/import (ImportHistory), the post-pairing
// auto import and the lost-messages cron (run with the window cut). Without
// the import URI every run stays inert (0, nil); a disabled connector or a
// missing session also imports nothing.
type chatwootHistoryImporter struct {
	configs     storage.ChatwootConfigRepository
	sessions    session.Manager
	clientFor   func(cfg model.ChatwootConfig) chatimport.InboxLister
	notify      func(ctx context.Context, instanceID uuid.UUID, text string) error
	placeholder bool
}

// ImportHistory runs the manual import of instanceID.
func (a *chatwootHistoryImporter) ImportHistory(ctx context.Context, instanceID uuid.UUID) (int, error) {
	return a.run(ctx, instanceID, time.Time{})
}

// run imports the feed snapshot of instanceID, cutting history older than
// since when set (the lost-messages window; zero takes the full days_limit
// window).
func (a *chatwootHistoryImporter) run(ctx context.Context, instanceID uuid.UUID, since time.Time) (int, error) {
	pool, ok := chatimport.Pool(ctx)
	if !ok {
		return 0, nil
	}
	cfg, err := a.configs.Get(ctx, instanceID)
	if err != nil {
		return 0, err
	}
	if cfg == nil || !cfg.Enabled {
		return 0, nil
	}
	sess, ok := a.sessions.Get(instanceID)
	if !ok {
		return 0, fmt.Errorf("chatimport: run import: no session for instance %s", instanceID)
	}
	var poster func(ctx context.Context, text string) error
	if a.notify != nil {
		poster = func(ctx context.Context, text string) error {
			return a.notify(ctx, instanceID, text)
		}
	}
	return chatimport.RunImport(ctx, chatimport.RunDeps{
		Pool:        pool,
		Config:      *cfg,
		Inboxes:     a.clientFor(*cfg),
		Feed:        sess,
		Placeholder: a.placeholder,
		NewerThan:   since,
		Poster:      poster,
	})
}

// sessionQRProvider adapts the session manager to the mirror QRProvider:
// pairing notices fetch the live QR string from the session's in-memory
// pairing state at handle time. A missing session surfaces as a lookup
// error, which the mirror degrades to the text-only notice.
type sessionQRProvider struct{ sessions session.Manager }

func (p sessionQRProvider) QRCode(ctx context.Context, instanceID uuid.UUID) (string, time.Time, error) {
	sess, ok := p.sessions.Get(instanceID)
	if !ok {
		return "", time.Time{}, fmt.Errorf("qr provider: no session for instance %s", instanceID)
	}
	return sess.QR(ctx)
}

// miswiredContactResolver fails every resolution with the wiring error so a
// factory type mistake never panics; the boot check in serve should already
// have failed closed before the worker starts.
type miswiredContactResolver struct{ err error }

func (r miswiredContactResolver) Resolve(context.Context, string, bool, string, string, string) (*contacts.Contact, error) {
	return nil, r.err
}

// miswiredConversationResolver fails every resolution with the wiring error so
// a factory type mistake never panics; the boot check in serve should already
// have failed closed before the worker starts.
type miswiredConversationResolver struct{ err error }

func (r miswiredConversationResolver) Resolve(context.Context, uuid.UUID, string, int64) (int64, error) {
	return 0, r.err
}

// stopComponents stops the workers in order, waiting for each one within ctx so
// a worker slow to return cannot extend the shutdown beyond the deadline.
func stopComponents(ctx context.Context, log *slog.Logger, components ...shutdownComponent) {
	for _, component := range components {
		component.stop()
		select {
		case <-component.done:
		case <-ctx.Done():
			log.Warn("shutdown wait timed out", "component", component.name)
		}
	}
}

// migrate applies the pending migrations and exits.
func migrate() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	fmt.Println("migrations applied")
	return nil
}

// healthcheck calls the local readiness endpoint and exits 0 when the service
// is ready or 1 otherwise.
func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	url, err := readyURL(cfg.HTTPAddr)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("healthcheck %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// readyURL builds the loopback readiness URL for a configured HTTP address,
// replacing empty or wildcard hosts with 127.0.0.1.
func readyURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid HTTP address %q: %w", addr, err)
	}

	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}

// newLogger builds the structured logger from the configuration.
func newLogger(cfg config.Config) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("invalid WZAP_LOG_LEVEL %q: %w", cfg.LogLevel, err)
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch cfg.LogFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, fmt.Errorf("invalid WZAP_LOG_FORMAT %q, want json or text", cfg.LogFormat)
	}
	return slog.New(handler), nil
}
