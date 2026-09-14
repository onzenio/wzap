// Package chatimport retrofills WhatsApp history into Chatwoot through direct
// Postgres access, recreating the Evolution import behavior without copying
// it. Everything here is inert without WZAP_CHATWOOT_IMPORT_DB_URL: Pool
// reports ok=false and ImportContacts is a no-op, so the rest of the
// connector keeps working while the import stays disabled.
package chatimport

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// envImportDBURL names the Chatwoot Postgres URI. Empty means unset: the
// import is disabled. The same value is also exposed as
// Config.Chatwoot.ImportDBURL for boot wiring.
const envImportDBURL = "WZAP_CHATWOOT_IMPORT_DB_URL"

var (
	poolMu   sync.Mutex
	poolOnce sync.Once
	pool     *pgxpool.Pool
)

// Pool returns the process-wide Chatwoot import pool. ok=false means the
// import is inert: the URI is unset, empty or unparseable, and zero network
// I/O was attempted. The pool is lazy: a syntactically valid but unreachable
// URI still reports ok=true and surfaces as an operation error on first use.
//
// The singleton binds to the first non-empty URI seen in the process; the
// service URI is static at boot, so this never rebinds in production. SSL
// follows the URI: verification is only skipped when the URI itself asks for
// it (for example sslmode=disable), same regime as the Evolution import.
func Pool(ctx context.Context) (*pgxpool.Pool, bool) {
	uri := strings.TrimSpace(os.Getenv(envImportDBURL))
	if uri == "" {
		return nil, false
	}
	poolOnce.Do(func() {
		p, err := pgxpool.New(ctx, uri)
		if err != nil {
			return
		}
		poolMu.Lock()
		pool = p
		poolMu.Unlock()
	})
	poolMu.Lock()
	defer poolMu.Unlock()
	if pool == nil {
		return nil, false
	}
	return pool, true
}

// Close releases the shared import pool, if any. Shutdown wiring arrives in a
// later task; this is exposed now so the singleton has a single release path.
// Close must not run concurrently with Pool.
func Close() {
	poolMu.Lock()
	defer poolMu.Unlock()
	if pool != nil {
		pool.Close()
		pool = nil
	}
	poolOnce = sync.Once{}
}

// resetPoolForTest drops the singleton between tests; tests only.
func resetPoolForTest(t *testing.T) {
	t.Helper()
	Close()
}
