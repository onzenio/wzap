package whatsmeow

import (
	"context"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the pure-Go pgx database/sql driver
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// postgresDialect is the database/sql driver exposed by pgx's stdlib adapter.
// sqlstore maps it to its Postgres dialect; the driver is pure Go, so the
// service still builds with CGO_ENABLED=0.
const postgresDialect = "pgx"

// openDeviceStore opens the whatsmeow session tables in the Postgres database
// addressed by dsn, creating or upgrading them as needed.
func openDeviceStore(ctx context.Context, dsn string, log *slog.Logger) (*sqlstore.Container, error) {
	container, err := sqlstore.New(ctx, postgresDialect, dsn, newWALogger(log))
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow device store: %w", err)
	}
	return container, nil
}

// waLogger adapts a slog.Logger to the logger interface expected by whatsmeow,
// keeping the library's logging in the service's structured output.
type waLogger struct {
	log    *slog.Logger
	module string
}

var _ waLog.Logger = (*waLogger)(nil)

// newWALogger returns the whatsmeow logger writing through slog.
func newWALogger(log *slog.Logger) waLog.Logger {
	if log == nil {
		log = slog.Default()
	}
	return &waLogger{log: log, module: "whatsmeow"}
}

func (l *waLogger) Debugf(msg string, args ...any) {
	l.log.Debug(fmt.Sprintf(msg, args...), "module", l.module)
}

func (l *waLogger) Infof(msg string, args ...any) {
	l.log.Info(fmt.Sprintf(msg, args...), "module", l.module)
}

func (l *waLogger) Warnf(msg string, args ...any) {
	l.log.Warn(fmt.Sprintf(msg, args...), "module", l.module)
}

func (l *waLogger) Errorf(msg string, args ...any) {
	l.log.Error(fmt.Sprintf(msg, args...), "module", l.module)
}

func (l *waLogger) Sub(module string) waLog.Logger {
	return &waLogger{log: l.log, module: l.module + "." + module}
}
