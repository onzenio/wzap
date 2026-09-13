package postgres

import (
	"context"
	"os"
	"testing"
)

func TestConnect(t *testing.T) {
	databaseURL := os.Getenv("WZAP_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set WZAP_TEST_DATABASE_URL to run the Postgres integration test")
	}

	ctx := context.Background()
	pool, err := Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if got := pool.Config().MaxConns; got != maxConns {
		t.Errorf("MaxConns = %d, want %d", got, maxConns)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
