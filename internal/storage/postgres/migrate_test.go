package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"wzap/internal/storage/migrations"
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

type columnInfo struct {
	dataType   string
	udtName    string
	isNullable string
	columnDef  *string
}

func tableColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) map[string]columnInfo {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type, udt_name, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		t.Fatalf("query columns of %s: %v", table, err)
	}
	defer rows.Close()

	cols := map[string]columnInfo{}
	for rows.Next() {
		var name string
		var c columnInfo
		if err := rows.Scan(&name, &c.dataType, &c.udtName, &c.isNullable, &c.columnDef); err != nil {
			t.Fatalf("scan column of %s: %v", table, err)
		}
		cols[name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns of %s: %v", table, err)
	}
	return cols
}

func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1
		)`, table).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return exists
}

// TestMigrateProductFresh applies every migration on a clean database and
// checks the product schema: the users table and the new instances columns.
func TestMigrateProductFresh(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if !tableExists(t, ctx, pool, "users") {
		t.Fatal("table users does not exist after Migrate")
	}

	users := tableColumns(t, ctx, pool, "users")
	for _, col := range []string{"id", "email", "password_hash", "role", "instance_quota", "created_at", "updated_at"} {
		if _, ok := users[col]; !ok {
			t.Errorf("users.%s column is missing", col)
		}
	}
	if got := users["instance_quota"]; got.dataType != "integer" || got.isNullable != "NO" {
		t.Errorf("users.instance_quota = type %s nullable %s, want integer NOT NULL", got.dataType, got.isNullable)
	}
	if def := users["instance_quota"].columnDef; def == nil || *def != "0" {
		t.Errorf("users.instance_quota default = %v, want 0", def)
	}
	// owner stays logically required later; the migration keeps it out of users.

	var checkDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = to_regclass(current_schema() || '.users')
		  AND contype = 'c'`).Scan(&checkDef); err != nil {
		t.Fatalf("read users CHECK constraint: %v", err)
	}
	if !strings.Contains(checkDef, "'admin'") || !strings.Contains(checkDef, "'user'") {
		t.Errorf("users role CHECK constraint = %q, want admin/user membership", checkDef)
	}

	var indexDef string
	if err := pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = 'users'
		  AND indexdef ILIKE '%lower(email)%'`).Scan(&indexDef); err != nil {
		t.Fatalf("unique index on lower(email) is missing: %v", err)
	}
	if !strings.Contains(strings.ToUpper(indexDef), "UNIQUE") {
		t.Errorf("index on lower(email) = %q, want UNIQUE", indexDef)
	}

	instances := tableColumns(t, ctx, pool, "instances")
	for _, col := range []string{"owner_user_id", "api_key_hash", "webhook_url", "webhook_enabled", "webhook_events"} {
		if _, ok := instances[col]; !ok {
			t.Errorf("instances.%s column is missing", col)
		}
	}
	// R4: owner stays NULLABLE in this migration; NOT NULL comes later.
	if got := instances["owner_user_id"]; got.udtName != "uuid" || got.isNullable != "YES" {
		t.Errorf("instances.owner_user_id = type %s nullable %s, want uuid NULL", got.udtName, got.isNullable)
	}
	if got := instances["webhook_enabled"]; got.dataType != "boolean" || got.isNullable != "NO" {
		t.Errorf("instances.webhook_enabled = type %s nullable %s, want boolean NOT NULL", got.dataType, got.isNullable)
	}
	if def := mustDef(t, instances["webhook_enabled"].columnDef, "instances.webhook_enabled"); def != "false" {
		t.Errorf("instances.webhook_enabled default = %q, want false", def)
	}
	if got := instances["webhook_events"]; got.udtName != "_text" || got.isNullable != "NO" {
		t.Errorf("instances.webhook_events = type %s nullable %s, want text[] NOT NULL", got.udtName, got.isNullable)
	}
	if def := mustDef(t, instances["webhook_events"].columnDef, "instances.webhook_events"); !strings.Contains(def, "message") ||
		!strings.Contains(def, "receipt") || !strings.Contains(def, "connection") || !strings.Contains(def, "message.status") {
		t.Errorf("instances.webhook_events default = %q, want message,receipt,connection,message.status", def)
	}

	// Behavior: case-insensitive email uniqueness, role check, FK, defaults.
	adminID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, $3, 'admin')`,
		adminID, "Admin@Example.com", "hash"); err != nil {
		t.Fatalf("insert admin user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, $3, 'user')`,
		uuid.New(), "admin@example.com", "hash"); err == nil {
		t.Error("duplicate email with different case was accepted, want unique lower(email)")
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, $3, 'superadmin')`,
		uuid.New(), "other@example.com", "hash"); err == nil {
		t.Error("role 'superadmin' was accepted, want CHECK rejection")
	}

	instanceID := uuid.New()
	var enabled bool
	var events []string
	var owner *string
	if err := pool.QueryRow(ctx, `
		INSERT INTO instances (id, name, owner_user_id) VALUES ($1, 'loja', $2)
		RETURNING webhook_enabled, webhook_events, owner_user_id::text`,
		instanceID, adminID).Scan(&enabled, &events, &owner); err != nil {
		t.Fatalf("insert owned instance: %v", err)
	}
	if enabled {
		t.Error("webhook_enabled default = true, want false")
	}
	if len(events) != 4 || events[0] != "message" || events[1] != "receipt" || events[2] != "connection" || events[3] != "message.status" {
		t.Errorf("webhook_events default = %v, want [message receipt connection message.status]", events)
	}
	if owner == nil || *owner != adminID.String() {
		t.Errorf("owner_user_id = %v, want %s", owner, adminID)
	}

	// Legacy-style row without an owner is still accepted here (R4).
	if _, err := pool.Exec(ctx,
		`INSERT INTO instances (id, name) VALUES ($1, 'legacy-style')`, uuid.New()); err != nil {
		t.Fatalf("insert ownerless instance: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO instances (id, name, owner_user_id) VALUES ($1, 'bad-owner', $2)`,
		uuid.New(), uuid.New()); err == nil {
		t.Error("unknown owner_user_id was accepted, want FK rejection")
	}
}

// TestMigrateProductLegacyInstances migrates only 00001, inserts legacy
// instance rows, then runs the full Migrate and checks the rows survive with
// NULL owner and product defaults.
func TestMigrateProductLegacyInstances(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.UpToContext(ctx, sqlDB, ".", 1); err != nil {
		t.Fatalf("migrate up to 00001: %v", err)
	}
	if tableExists(t, ctx, pool, "users") {
		t.Fatal("table users exists after 00001, want it created only by 00002")
	}

	legacyID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO instances (id, name, external_ref) VALUES ($1, 'legacy', 'ext-1')`,
		legacyID); err != nil {
		t.Fatalf("insert legacy instance: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !tableExists(t, ctx, pool, "users") {
		t.Fatal("table users does not exist after Migrate")
	}

	var name string
	var extRef *string
	var owner *string
	var apiKeyHash *string
	var webhookURL *string
	var enabled bool
	var events []string
	if err := pool.QueryRow(ctx, `
		SELECT name, external_ref, owner_user_id::text, api_key_hash,
		       webhook_url, webhook_enabled, webhook_events
		FROM instances WHERE id = $1`, legacyID).
		Scan(&name, &extRef, &owner, &apiKeyHash, &webhookURL, &enabled, &events); err != nil {
		t.Fatalf("read legacy instance: %v", err)
	}
	if name != "legacy" || extRef == nil || *extRef != "ext-1" {
		t.Errorf("legacy row changed: name=%q external_ref=%v", name, extRef)
	}
	if owner != nil {
		t.Errorf("legacy owner_user_id = %v, want NULL", *owner)
	}
	if apiKeyHash != nil || webhookURL != nil {
		t.Errorf("legacy api_key_hash=%v webhook_url=%v, want NULL", apiKeyHash, webhookURL)
	}
	if enabled {
		t.Error("legacy webhook_enabled = true, want false")
	}
	if len(events) != 4 {
		t.Errorf("legacy webhook_events = %v, want 4 default entries", events)
	}
}

func mustDef(t *testing.T, def *string, col string) string {
	t.Helper()
	if def == nil {
		t.Fatalf("%s has no column default", col)
	}
	return *def
}
