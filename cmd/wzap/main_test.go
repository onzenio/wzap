package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestReadyURL(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		want    string
		wantErr bool
	}{
		{name: "port only", addr: ":8080", want: "http://127.0.0.1:8080/readyz"},
		{name: "any host", addr: "0.0.0.0:8080", want: "http://127.0.0.1:8080/readyz"},
		{name: "ipv6 any host", addr: "[::]:8080", want: "http://127.0.0.1:8080/readyz"},
		{name: "explicit loopback", addr: "127.0.0.1:9001", want: "http://127.0.0.1:9001/readyz"},
		{name: "missing port", addr: "8080", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readyURL(tt.addr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("readyURL(%q) = %q, want error", tt.addr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readyURL(%q): %v", tt.addr, err)
			}
			if got != tt.want {
				t.Errorf("readyURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestHealthcheck(t *testing.T) {
	t.Setenv("WZAP_SERVICE_TOKEN", "token")
	t.Setenv("WZAP_DATABASE_URL", "postgres://example/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://example:4222")

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "ready", status: http.StatusOK},
		{name: "unready", status: http.StatusServiceUnavailable, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" {
					t.Errorf("path = %q, want %q", r.URL.Path, "/readyz")
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			t.Setenv("WZAP_HTTP_ADDR", srv.Listener.Addr().String())

			err := healthcheck()
			if tt.wantErr && err == nil {
				t.Fatal("healthcheck succeeded against an unready service")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("healthcheck failed against a ready service: %v", err)
			}
		})
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("run accepted an unknown subcommand")
	}
}

func TestStopComponentsStopsInOrder(t *testing.T) {
	done := make(chan struct{})
	close(done)
	var order []string

	stopComponents(context.Background(), slog.Default(),
		shutdownComponent{name: "outbox", stop: func() { order = append(order, "outbox") }, done: done},
		shutdownComponent{name: "media cleaner", stop: func() { order = append(order, "media cleaner") }, done: done},
		shutdownComponent{name: "event relay", stop: func() { order = append(order, "event relay") }, done: done},
	)

	want := []string{"outbox", "media cleaner", "event relay"}
	if !slices.Equal(order, want) {
		t.Errorf("stop order = %v, want %v", order, want)
	}
}

func TestStopComponentsBoundsTheWait(t *testing.T) {
	never := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	stopComponents(ctx, slog.Default(),
		shutdownComponent{name: "stuck", stop: func() {}, done: never},
	)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("stopComponents waited %v, want it to return on the context", elapsed)
	}
}
