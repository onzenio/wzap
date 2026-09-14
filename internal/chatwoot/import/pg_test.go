package chatimport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/session"
)

// TestPoolInertWithoutURI is the guard: without WZAP_CHATWOOT_IMPORT_DB_URL
// the import stays inert, Pool reports ok=false and zero network I/O is
// attempted.
func TestPoolInertWithoutURI(t *testing.T) {
	t.Setenv(envImportDBURL, "")
	resetPoolForTest(t)

	pool, ok := Pool(context.Background())
	if ok {
		t.Fatalf("Pool() ok = true, want false without %s", envImportDBURL)
	}
	if pool != nil {
		t.Fatalf("Pool() = %v, want nil without %s", pool, envImportDBURL)
	}
}

// TestPoolUnparseableURIStaysInert documents that a malformed URI keeps the
// import inert instead of dialing anywhere.
func TestPoolUnparseableURIStaysInert(t *testing.T) {
	t.Setenv(envImportDBURL, "://bad-uri")
	resetPoolForTest(t)

	pool, ok := Pool(context.Background())
	if ok {
		t.Fatalf("Pool() ok = true, want false for unparseable URI")
	}
	if pool != nil {
		t.Fatalf("Pool() = %v, want nil for unparseable URI", pool)
	}
}

// TestImportContactsInertWithoutPool is the other half of the guard: a nil
// pool (import disabled) imports nothing and touches no network.
func TestImportContactsInertWithoutPool(t *testing.T) {
	contacts := []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	}

	got, err := ImportContacts(context.Background(), nil, "1", "Inbox", contacts)
	if err != nil {
		t.Fatalf("ImportContacts() error = %v, want nil when inert", err)
	}
	if got != 0 {
		t.Fatalf("ImportContacts() = %d, want 0 when inert", got)
	}
}

// TestImportContactsUnreachablePoolNamesOperation: a syntactically valid but
// unreachable URI builds a lazy pool, and the first real use fails with an
// error naming the operation.
func TestImportContactsUnreachablePoolNamesOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig("postgres://127.0.0.1:1/nope?sslmode=disable")
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	bad, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	defer bad.Close()

	contacts := []session.HistorySyncContact{
		{JID: "5511999887766@s.whatsapp.net", Name: "Alice"},
	}

	if _, err := ImportContacts(ctx, bad, "1", "Inbox", contacts); err == nil {
		t.Fatal("ImportContacts() error = nil, want error against unreachable database")
	} else if !strings.Contains(err.Error(), "import contacts") {
		t.Fatalf("ImportContacts() error = %q, want it to name the operation", err)
	}
}
