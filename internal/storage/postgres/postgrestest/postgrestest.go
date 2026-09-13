// Package postgrestest provides race-safe Postgres fixtures for integration
// tests.
//
// Every pool returned by NewPool is bound to a uniquely named schema inside the
// WZAP_TEST_DATABASE_URL database, so test binaries running in parallel (or
// repeated runs) cannot drop or migrate tables under each other. The schema is
// dropped when the test finishes.
//
// Tests that need the wzap schema call postgres.Migrate on the returned pool.
// This package deliberately does not import the postgres package, so tests
// inside it can keep using this helper.
package postgrestest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const envDatabaseURL = "WZAP_TEST_DATABASE_URL"

// NewPool returns a connection pool bound to a fresh, uniquely named schema of
// the WZAP_TEST_DATABASE_URL database. The test is skipped when the variable is
// unset; the test fails when the database name does not end in _test.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		t.Skipf("set %s to run Postgres integration tests", envDatabaseURL)
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse %s: %v", envDatabaseURL, err)
	}
	if err := validateDatabaseName(cfg.ConnConfig.Database); err != nil {
		t.Fatalf("invalid %s: %v", envDatabaseURL, err)
	}

	schema := newSchemaName(t)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}

	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(setupCtx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		pool.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		pool.Close()
	})

	t.Logf("postgrestest: using schema %s", schema)

	return pool
}

func validateDatabaseName(database string) error {
	if !strings.HasSuffix(database, "_test") {
		return fmt.Errorf("refusing to run against database %q: name must end in _test", database)
	}
	return nil
}

func newSchemaName(t testing.TB) string {
	t.Helper()

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate schema name: %v", err)
	}
	return "wzap_test_" + hex.EncodeToString(suffix[:])
}
