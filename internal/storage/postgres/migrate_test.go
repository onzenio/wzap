package postgres

import (
	"context"
	"testing"

	"wzap/internal/storage/postgres/postgrestest"
)

func TestMigrate(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	for _, table := range []string{
		"instances",
		"message_queue",
		"idempotency_keys",
		"contacts",
		"media",
		"event_outbox",
	} {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = current_schema() AND table_name = $1
			)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after Migrate", table)
		}
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate (idempotence): %v", err)
	}
}
