package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wzap/internal/config"
	"wzap/internal/storage/postgres"
	"wzap/internal/storage/postgres/postgrestest"
)

// checkFunc adapts a plain function to the ReadyChecker interface.
type checkFunc func(ctx context.Context) error

func (f checkFunc) Check(ctx context.Context) error { return f(ctx) }

// detailedChecker reports one result per dependency.
type detailedChecker struct {
	results map[string]error
}

func (d detailedChecker) Check(context.Context) error {
	var failures []error
	for name, err := range d.results {
		if err != nil {
			failures = append(failures, errors.New(name+": "+err.Error()))
		}
	}
	return errors.Join(failures...)
}

func (d detailedChecker) Checks(context.Context) map[string]error { return d.results }

func readyServer(t *testing.T, checker ReadyChecker) *http.Server {
	t.Helper()
	return New(config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken}, discardLogger(),
		Deps{ReadyChecker: checker})
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		checker    ReadyChecker
		wantStatus int
		wantState  string
	}{
		{
			name:       "dependency ready",
			checker:    checkFunc(func(context.Context) error { return nil }),
			wantStatus: http.StatusOK,
			wantState:  "ready",
		},
		{
			name:       "dependency failed",
			checker:    checkFunc(func(context.Context) error { return errors.New("connection refused") }),
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "unready",
		},
		{
			name:       "checker missing",
			checker:    nil,
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "unready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, readyServer(t, tt.checker), http.MethodGet, "/readyz", "")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := dataField(t, rec.Body.Bytes(), "status"); got != tt.wantState {
				t.Errorf("data.status = %q, want %q", got, tt.wantState)
			}
		})
	}
}

func TestReadyzReportsChecks(t *testing.T) {
	tests := []struct {
		name       string
		results    map[string]error
		wantStatus int
		wantState  string
		wantChecks map[string]string
	}{
		{
			name:       "all dependencies ready",
			results:    map[string]error{"postgres": nil, "migrations": nil},
			wantStatus: http.StatusOK,
			wantState:  "ready",
			wantChecks: map[string]string{"postgres": "ok", "migrations": "ok"},
		},
		{
			name:       "one dependency failed",
			results:    map[string]error{"postgres": nil, "migrations": errors.New("pending migration 2")},
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "unready",
			wantChecks: map[string]string{"postgres": "ok", "migrations": "failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, readyServer(t, detailedChecker{results: tt.results}), http.MethodGet, "/readyz", "")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var payload struct {
				Data struct {
					Status string            `json:"status"`
					Checks map[string]string `json:"checks"`
				} `json:"data"`
			}
			decodeJSON(t, rec.Body.Bytes(), &payload)

			if payload.Data.Status != tt.wantState {
				t.Errorf("data.status = %q, want %q", payload.Data.Status, tt.wantState)
			}
			for name, want := range tt.wantChecks {
				if got := payload.Data.Checks[name]; got != want {
					t.Errorf("checks.%s = %q, want %q", name, got, want)
				}
			}
			if body := rec.Body.String(); strings.Contains(body, "pending migration 2") {
				t.Errorf("readiness response leaks the probe error: %s", body)
			}
		})
	}
}

func TestCheckerAggregatesProbes(t *testing.T) {
	probeError := errors.New("probe unavailable")
	checker := &Checker{probes: []probe{
		{name: "first", run: func(context.Context) error { return nil }},
		{name: "second", run: func(context.Context) error { return probeError }},
	}}

	err := checker.Check(context.Background())
	if err == nil {
		t.Fatal("Check succeeded with a failing probe")
	}
	if !strings.Contains(err.Error(), "second") {
		t.Errorf("Check error = %q, want it to name the failing probe", err)
	}

	results := checker.Checks(context.Background())
	if results["first"] != nil {
		t.Errorf("first probe result = %v, want nil", results["first"])
	}
	if !errors.Is(results["second"], probeError) {
		t.Errorf("second probe result = %v, want %v", results["second"], probeError)
	}
}

func TestNewCheckerRunsNamedProbes(t *testing.T) {
	pool := postgrestest.NewPool(t)
	probeErr := errors.New("broker unavailable")
	checker := NewChecker(pool,
		NamedProbe{Name: "broker", Run: func(context.Context) error { return probeErr }},
		NamedProbe{Name: "cache", Run: func(context.Context) error { return nil }},
	)

	ctx := context.Background()
	results := checker.Checks(ctx)
	if !errors.Is(results["broker"], probeErr) {
		t.Errorf("broker probe result = %v, want %v", results["broker"], probeErr)
	}
	if results["cache"] != nil {
		t.Errorf("cache probe result = %v, want nil", results["cache"])
	}
	if err := checker.Check(ctx); err == nil || !strings.Contains(err.Error(), "broker") {
		t.Errorf("Check error = %v, want it to name the failing broker probe", err)
	}
}

func TestCheckerReportsMigrationState(t *testing.T) {
	pool := postgrestest.NewPool(t)
	ctx := context.Background()
	checker := NewChecker(pool)

	results := checker.Checks(ctx)
	if err := results["postgres"]; err != nil {
		t.Fatalf("postgres probe failed on a live pool: %v", err)
	}
	if err := results["migrations"]; err == nil {
		t.Error("migrations probe succeeded before any migration was applied")
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := checker.Check(ctx); err != nil {
		t.Fatalf("Check failed after migrations: %v", err)
	}
	results = checker.Checks(ctx)
	if err := results["migrations"]; err != nil {
		t.Errorf("migrations probe failed after Migrate: %v", err)
	}
}

func TestReadyzLogsFailureWithRequestID(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	srv := New(config.Config{HTTPAddr: "127.0.0.1:0", APIKey: testToken}, log,
		Deps{ReadyChecker: checkFunc(func(context.Context) error { return errors.New("dependency down") })})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set(requestIDHeader, "req-abc")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)

	var warning string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "readiness check failed") {
			warning = line
		}
	}
	if warning == "" {
		t.Fatalf("readiness failure was not logged: %s", logs.String())
	}
	if !strings.Contains(warning, "request_id=req-abc") {
		t.Errorf("warning is missing the request id: %s", warning)
	}
	if !strings.Contains(warning, "dependency down") {
		t.Errorf("warning is missing the probe error: %s", warning)
	}
}
