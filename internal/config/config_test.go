package config

import (
	"strings"
	"testing"
)

var allEnvKeys = []string{
	"WZAP_HTTP_ADDR",
	"WZAP_PUBLIC_URL",
	"WZAP_API_KEY",
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
	"WZAP_ADMIN_EMAIL",
	"WZAP_ADMIN_PASSWORD",
	"WZAP_JWT_SECRET",
	"WZAP_MAX_INSTANCES",
	"WZAP_DEFAULT_USER_INSTANCE_QUOTA",
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
	t.Setenv("WZAP_API_KEY", "test-api-key")
	t.Setenv("WZAP_JWT_SECRET", "test-jwt-secret")
	t.Setenv("WZAP_DATABASE_URL", "postgres://wzap:secret@127.0.0.1:5432/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://127.0.0.1:4222")
}

func TestLoadFullConfig(t *testing.T) {
	clearWZAPEnv(t)
	for key, value := range map[string]string{
		"WZAP_HTTP_ADDR":                   "127.0.0.1:9999",
		"WZAP_PUBLIC_URL":                  "https://wzap.example.com",
		"WZAP_API_KEY":                     "api-key",
		"WZAP_ADMIN_EMAIL":                 "admin@example.com",
		"WZAP_ADMIN_PASSWORD":              "s3cret",
		"WZAP_JWT_SECRET":                  "jwt-secret",
		"WZAP_MAX_INSTANCES":               "10",
		"WZAP_DEFAULT_USER_INSTANCE_QUOTA": "3",
		"WZAP_DATABASE_URL":                "postgres://wzap:secret@db:5432/wzap",
		"WZAP_NATS_URL":                    "nats://nats:4222",
		"WZAP_NATS_STREAM":                 "WZAP_TEST",
		"WZAP_EVENT_RETENTION_DAYS":        "30",
		"WZAP_DATA_DIR":                    "/tmp/wzap",
		"WZAP_MEDIA_TTL_SECONDS":           "60",
		"WZAP_MAX_MEDIA_BYTES":             "1024",
		"WZAP_OUTBOX_WORKERS":              "2",
		"WZAP_HUMANIZE":                    "true",
		"WZAP_LOG_LEVEL":                   "debug",
		"WZAP_LOG_FORMAT":                  "text",
		"WZAP_AUTO_MIGRATE":                "false",
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
		APIKey:             "api-key",
		AdminEmail:         "admin@example.com",
		AdminPassword:      "s3cret",
		JWTSecret:          "jwt-secret",
		MaxInstances:       10,
		DefaultUserQuota:   3,
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
		APIKey:             "test-api-key",
		AdminEmail:         "",
		AdminPassword:      "",
		JWTSecret:          "test-jwt-secret",
		MaxInstances:       0,
		DefaultUserQuota:   0,
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

func TestLoadMissingAPIKey(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_JWT_SECRET", "jwt-secret")
	t.Setenv("WZAP_DATABASE_URL", "postgres://wzap:secret@127.0.0.1:5432/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://127.0.0.1:4222")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error naming WZAP_API_KEY")
	}
	if !strings.Contains(err.Error(), "WZAP_API_KEY") {
		t.Errorf("Load() error = %q, want it to mention WZAP_API_KEY", err)
	}
}

func TestLoadMissingJWTSecret(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_API_KEY", "api-key")
	t.Setenv("WZAP_DATABASE_URL", "postgres://wzap:secret@127.0.0.1:5432/wzap")
	t.Setenv("WZAP_NATS_URL", "nats://127.0.0.1:4222")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error naming WZAP_JWT_SECRET")
	}
	if !strings.Contains(err.Error(), "WZAP_JWT_SECRET") {
		t.Errorf("Load() error = %q, want it to mention WZAP_JWT_SECRET", err)
	}
}

func TestLoadEmptyRequiredTreatedAsMissing(t *testing.T) {
	for _, key := range []string{"WZAP_API_KEY", "WZAP_JWT_SECRET"} {
		t.Run(key, func(t *testing.T) {
			clearWZAPEnv(t)
			setRequiredEnv(t)
			t.Setenv(key, "")

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want error naming %s", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("Load() error = %q, want it to mention %s", err, key)
			}
		})
	}
}

func TestLoadIgnoresLegacyServiceToken(t *testing.T) {
	clearWZAPEnv(t)
	setRequiredEnv(t)
	t.Setenv("WZAP_SERVICE_TOKEN", "legacy-token")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want lingering WZAP_SERVICE_TOKEN to be silently ignored", err)
	}
	if got.APIKey != "test-api-key" {
		t.Errorf("Load().APIKey = %q, want %q", got.APIKey, "test-api-key")
	}
}

func TestLoadExplicitZeroQuotasMeanUnlimited(t *testing.T) {
	clearWZAPEnv(t)
	setRequiredEnv(t)
	t.Setenv("WZAP_MAX_INSTANCES", "0")
	t.Setenv("WZAP_DEFAULT_USER_INSTANCE_QUOTA", "0")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.MaxInstances != 0 {
		t.Errorf("Load().MaxInstances = %d, want 0 (unlimited)", got.MaxInstances)
	}
	if got.DefaultUserQuota != 0 {
		t.Errorf("Load().DefaultUserQuota = %d, want 0 (unlimited)", got.DefaultUserQuota)
	}
}

func TestLoadRejectsNegativeQuotas(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"WZAP_MAX_INSTANCES", "-1"},
		{"WZAP_DEFAULT_USER_INSTANCE_QUOTA", "-2"},
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
		})
	}
}

func TestLoadRejectsNonNumericQuotas(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"WZAP_MAX_INSTANCES", "many"},
		{"WZAP_DEFAULT_USER_INSTANCE_QUOTA", "lots"},
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

func TestLoadMissingDatabaseAndNATS(t *testing.T) {
	clearWZAPEnv(t)
	t.Setenv("WZAP_API_KEY", "api-key")
	t.Setenv("WZAP_JWT_SECRET", "jwt-secret")

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
		"WZAP_API_KEY", "WZAP_JWT_SECRET", "WZAP_DATABASE_URL", "WZAP_NATS_URL",
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
