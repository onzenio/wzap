package config

import (
	"strings"
	"testing"
)

var allEnvKeys = []string{
	"WZAP_HTTP_ADDR",
	"WZAP_PUBLIC_URL",
	"WZAP_SERVICE_TOKEN",
	"WZAP_DATABASE_URL",
	"WZAP_NATS_URL",
	"WZAP_NATS_STREAM",
	"WZAP_EVENT_RETENTION_DAYS",
	"WZAP_DATA_DIR",
	"WZAP_MEDIA_TTL_SECONDS",
	"WZAP_MAX_MEDIA_BYTES",
	"WZAP_OUTBOX_WORKERS",
	"WZAP_HUMANIZE",
	"WZAP_LOG_LEVEL",
	"WZAP_LOG_FORMAT",
	"WZAP_AUTO_MIGRATE",
}

// clearWZAPEnv clears every WZAP_* variable for the test, overriding and later
// restoring any ambient value. Empty is treated as unset by config.Load.
func clearWZAPEnv(t *testing.T) {
	t.Helper()
	for _, key := range allEnvKeys {
		t.Setenv(key, "")
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WZAP_SERVICE_TOKEN", "token")
	t.Setenv("WZAP_DATABASE_URL", "postgres://wzap:secret@127.0.0.1:5432/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://127.0.0.1:4222")
}

func TestLoadFullConfig(t *testing.T) {
	clearWZAPEnv(t)
	for key, value := range map[string]string{
		"WZAP_HTTP_ADDR":            "127.0.0.1:9999",
		"WZAP_PUBLIC_URL":           "https://wzap.example.com",
		"WZAP_SERVICE_TOKEN":        "token",
		"WZAP_DATABASE_URL":         "postgres://wzap:secret@db:5432/wzap",
		"WZAP_NATS_URL":             "nats://nats:4222",
		"WZAP_NATS_STREAM":          "WZAP_TEST",
		"WZAP_EVENT_RETENTION_DAYS": "30",
		"WZAP_DATA_DIR":             "/tmp/wzap",
		"WZAP_MEDIA_TTL_SECONDS":    "60",
		"WZAP_MAX_MEDIA_BYTES":      "1024",
		"WZAP_OUTBOX_WORKERS":       "2",
		"WZAP_HUMANIZE":             "true",
		"WZAP_LOG_LEVEL":            "debug",
		"WZAP_LOG_FORMAT":           "text",
		"WZAP_AUTO_MIGRATE":         "false",
	} {
		t.Setenv(key, value)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := Config{
		HTTPAddr:           "127.0.0.1:9999",
		PublicURL:          "https://wzap.example.com",
		ServiceToken:       "token",
		DatabaseURL:        "postgres://wzap:secret@db:5432/wzap",
		NATSURL:            "nats://nats:4222",
		NATSStream:         "WZAP_TEST",
		EventRetentionDays: 30,
		DataDir:            "/tmp/wzap",
		MediaTTLSeconds:    60,
		MaxMediaBytes:      1024,
		OutboxWorkers:      2,
		Humanize:           true,
		LogLevel:           "debug",
		LogFormat:          "text",
		AutoMigrate:        false,
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearWZAPEnv(t)
	setRequiredEnv(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := Config{
		HTTPAddr:           ":8080",
		PublicURL:          "",
		ServiceToken:       "token",
		DatabaseURL:        "postgres://wzap:secret@127.0.0.1:5432/wzap",
		NATSURL:            "nats://127.0.0.1:4222",
		NATSStream:         "WZAP",
		EventRetentionDays: 7,
		DataDir:            "/data",
		MediaTTLSeconds:    7200,
		MaxMediaBytes:      16777216,
		OutboxWorkers:      4,
		Humanize:           false,
		LogLevel:           "info",
		LogFormat:          "json",
		AutoMigrate:        true,
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadMissingServiceToken(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_DATABASE_URL", "postgres://wzap:secret@127.0.0.1:5432/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://127.0.0.1:4222")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error naming WZAP_SERVICE_TOKEN")
	}
	if !strings.Contains(err.Error(), "WZAP_SERVICE_TOKEN") {
		t.Errorf("Load() error = %q, want it to mention WZAP_SERVICE_TOKEN", err)
	}
}

func TestLoadMissingDatabaseAndNATS(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_SERVICE_TOKEN", "token")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error naming both missing variables")
	}
	for _, key := range []string{"WZAP_DATABASE_URL", "WZAP_NATS_URL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Load() error = %q, want it to mention %s", err, key)
		}
	}
}

func TestLoadRejectsNonPositiveNumbers(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"WZAP_EVENT_RETENTION_DAYS", "0"},
		{"WZAP_MEDIA_TTL_SECONDS", "-1"},
		{"WZAP_MAX_MEDIA_BYTES", "0"},
		{"WZAP_OUTBOX_WORKERS", "-2"},
	}

	for _, tt := range tests {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			clearWZAPEnv(t)
			setRequiredEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error naming %s", tt.key)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("Load() error = %q, want it to mention %s", err, tt.key)
			}
			if !strings.Contains(err.Error(), "positive") {
				t.Errorf("Load() error = %q, want it to say the value must be positive", err)
			}
		})
	}
}

func TestLoadAggregatesProblems(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_EVENT_RETENTION_DAYS", "0")
	t.Setenv("WZAP_MEDIA_TTL_SECONDS", "0")
	t.Setenv("WZAP_MAX_MEDIA_BYTES", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want the aggregated error")
	}
	for _, key := range []string{
		"WZAP_SERVICE_TOKEN", "WZAP_DATABASE_URL", "WZAP_NATS_URL",
		"WZAP_EVENT_RETENTION_DAYS", "WZAP_MEDIA_TTL_SECONDS", "WZAP_MAX_MEDIA_BYTES",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Load() error = %q, want it to mention %s", err, key)
		}
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"WZAP_EVENT_RETENTION_DAYS", "many"},
		{"WZAP_MEDIA_TTL_SECONDS", "soon"},
		{"WZAP_MAX_MEDIA_BYTES", "huge"},
		{"WZAP_OUTBOX_WORKERS", "all"},
		{"WZAP_HUMANIZE", "maybe"},
		{"WZAP_AUTO_MIGRATE", "perhaps"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			clearWZAPEnv(t)
			setRequiredEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error naming %s", tt.key)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("Load() error = %q, want it to mention %s", err, tt.key)
			}
		})
	}
}
