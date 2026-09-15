package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"wzap/internal/storage/migrations"
)

// Checker aggregates the readiness probes of the service dependencies: the
// Postgres ping and migration state plus any probe passed by the wiring layer
// (the broker connection check, for example).
type Checker struct {
	probes []probe
}

// probe is one named dependency check.
type probe struct {
	name string
	run  func(ctx context.Context) error
}

// NamedProbe is an additional readiness dependency check.
type NamedProbe struct {
	Name string
	Run  func(ctx context.Context) error
}

// NewChecker builds the readiness checker over the Postgres pool, appending
// the extra named probes after the built-in ones.
func NewChecker(pool *pgxpool.Pool, extra ...NamedProbe) *Checker {
	checker := &Checker{probes: []probe{
		{name: "postgres", run: pool.Ping},
		{name: "migrations", run: func(ctx context.Context) error {
			return migrationsApplied(ctx, pool)
		}},
	}}
	for _, add := range extra {
		checker.probes = append(checker.probes, probe{name: add.Name, run: add.Run})
	}
	return checker
}

// Check runs every probe and joins the failures into a single error.
func (c *Checker) Check(ctx context.Context) error {
	var failures []error
	for _, p := range c.probes {
		if err := p.run(ctx); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", p.name, err))
		}
	}
	return errors.Join(failures...)
}

// Checks runs every probe and returns the outcome of each one keyed by name.
func (c *Checker) Checks(ctx context.Context) map[string]error {
	results := make(map[string]error, len(c.probes))
	for _, p := range c.probes {
		results[p.name] = p.run(ctx)
	}
	return results
}

// migrationsApplied reports whether every embedded migration is applied. A
// missing goose version table means no migration has run yet.
func migrationsApplied(ctx context.Context, pool *pgxpool.Pool) error {
	count, latest, err := embeddedMigrations()
	if err != nil {
		return err
	}

	var applied int
	var appliedLatest int64
	row := pool.QueryRow(ctx, `
		SELECT count(DISTINCT version_id), coalesce(max(version_id), 0)
		FROM goose_db_version
		WHERE is_applied AND version_id > 0`)
	if err := row.Scan(&applied, &appliedLatest); err != nil {
		return fmt.Errorf("read goose version table: %w", err)
	}

	if applied < count || appliedLatest < latest {
		return fmt.Errorf("pending migrations: %d of %d applied, latest %d of %d",
			applied, count, appliedLatest, latest)
	}
	return nil
}

// embeddedMigrations returns how many SQL migrations are embedded and their
// highest version.
func embeddedMigrations() (int, int64, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0, 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	var count int
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return 0, 0, fmt.Errorf("migration %q has no version prefix", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("migration %q has invalid version: %w", entry.Name(), err)
		}
		count++
		latest = max(latest, version)
	}
	if count == 0 {
		return 0, 0, errors.New("no embedded migrations found")
	}
	return count, latest, nil
}

// readiness is the /readyz payload.
type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// detailedReadyChecker is implemented by checkers able to report each
// dependency individually.
type detailedReadyChecker interface {
	Checks(ctx context.Context) map[string]error
}

// handleHealthz reports liveness: the process is alive whenever it replies.
//
// @Summary Liveness probe
// @Description Reports liveness without authentication. The process is alive whenever it replies.
// @Tags health
// @Produce json
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Success 200 {string} string "Enveloped {\"status\": \"ok\"}"
// @Router /healthz [get]
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports readiness from the configured checker: 200 while every
// dependency is ready and 503 otherwise. The response names each dependency
// but never echoes probe errors.
//
// @Summary Readiness probe
// @Description Reports readiness without authentication: 200 while every dependency is ready, 503 otherwise. The response names each dependency but never echoes probe errors.
// @Tags health
// @Produce json
// @Param X-Request-Id header string false "Correlation id, echoed back"
// @Success 200 {object} readiness "Readiness, wrapped in the data envelope"
// @Failure 503 {object} readiness "Unready state, wrapped in the data envelope"
// @Router /readyz [get]
func handleReadyz(checker ReadyChecker, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		results := map[string]error{}
		var failures []error

		switch c := checker.(type) {
		case nil:
			failures = append(failures, errors.New("readiness checker is not configured"))
		case detailedReadyChecker:
			for name, err := range c.Checks(ctx) {
				results[name] = err
				if err != nil {
					failures = append(failures, fmt.Errorf("%s: %w", name, err))
				}
			}
		default:
			err := checker.Check(ctx)
			results["dependencies"] = err
			if err != nil {
				failures = append(failures, err)
			}
		}

		checks := make(map[string]string, len(results))
		for name, err := range results {
			if err != nil {
				checks[name] = "failed"
			} else {
				checks[name] = "ok"
			}
		}

		if len(failures) > 0 {
			log.WarnContext(ctx, "readiness check failed",
				"request_id", RequestIDFromContext(ctx),
				"error", errors.Join(failures...))
			JSON(w, r, http.StatusServiceUnavailable, readiness{Status: "unready", Checks: checks})
			return
		}
		JSON(w, r, http.StatusOK, readiness{Status: "ready", Checks: checks})
	}
}
