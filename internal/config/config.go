// Package config loads the wzap service configuration from environment
// variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the runtime settings of the wzap service.
type Config struct {
	HTTPAddr           string
	PublicURL          string
	APIKey             string
	AdminEmail         string
	AdminPassword      string
	JWTSecret          string
	MaxInstances       int
	DefaultUserQuota   int
	DatabaseURL        string
	NATSURL            string
	NATSStream         string
	EventRetentionDays int
	DataDir            string
	MediaTTLSeconds    int
	MaxMediaBytes      int64
	OutboxWorkers      int
	Humanize           bool
	LogLevel           string
	LogFormat          string
	AutoMigrate        bool
	Chatwoot           Chatwoot
}

// Chatwoot holds the global Chatwoot connector switches. BotContact is the
// operational contact identifier; empty means unset. ImportDBURL is the
// Chatwoot Postgres URI for history import; empty disables the import.
// ImportPlaceholder turns content-less history messages into a placeholder
// text instead of skipping them.
type Chatwoot struct {
	Enabled           bool
	BotContact        string
	MessageRead       bool
	MessageDelete     bool
	ImportDBURL       string
	ImportPlaceholder bool
}

const (
	defaultHTTPAddr           = ":8080"
	defaultNATSStream         = "WZAP"
	defaultEventRetentionDays = 7
	defaultDataDir            = "/data"
	defaultMediaTTLSeconds    = 7200
	defaultMaxMediaBytes      = 16777216
	defaultOutboxWorkers      = 4
	defaultLogLevel           = "info"
	defaultLogFormat          = "json"
	defaultAutoMigrate        = true
)

// Load reads the configuration from the environment, applies defaults for
// optional variables and returns an aggregated error when required variables
// are missing or values are malformed. Empty variables are treated as unset.
func Load() (Config, error) {
	var problems []string

	cfg := Config{
		HTTPAddr:      envOrDefault("WZAP_HTTP_ADDR", defaultHTTPAddr),
		PublicURL:     os.Getenv("WZAP_PUBLIC_URL"),
		APIKey:        os.Getenv("WZAP_API_KEY"),
		AdminEmail:    os.Getenv("WZAP_ADMIN_EMAIL"),
		AdminPassword: os.Getenv("WZAP_ADMIN_PASSWORD"),
		JWTSecret:     os.Getenv("WZAP_JWT_SECRET"),
		DatabaseURL:   os.Getenv("WZAP_DATABASE_URL"),
		NATSURL:       os.Getenv("WZAP_NATS_URL"),
		NATSStream:    envOrDefault("WZAP_NATS_STREAM", defaultNATSStream),
		DataDir:       envOrDefault("WZAP_DATA_DIR", defaultDataDir),
		LogLevel:      envOrDefault("WZAP_LOG_LEVEL", defaultLogLevel),
		LogFormat:     envOrDefault("WZAP_LOG_FORMAT", defaultLogFormat),
	}

	for _, required := range []struct {
		name  string
		value string
	}{
		{"WZAP_API_KEY", cfg.APIKey},
		{"WZAP_JWT_SECRET", cfg.JWTSecret},
		{"WZAP_DATABASE_URL", cfg.DatabaseURL},
		{"WZAP_NATS_URL", cfg.NATSURL},
	} {
		if required.value == "" {
			problems = append(problems, required.name+" is required")
		}
	}

	cfg.EventRetentionDays = positiveIntValue("WZAP_EVENT_RETENTION_DAYS", defaultEventRetentionDays, &problems)
	cfg.MediaTTLSeconds = positiveIntValue("WZAP_MEDIA_TTL_SECONDS", defaultMediaTTLSeconds, &problems)
	cfg.MaxMediaBytes = positiveInt64Value("WZAP_MAX_MEDIA_BYTES", defaultMaxMediaBytes, &problems)
	cfg.OutboxWorkers = positiveIntValue("WZAP_OUTBOX_WORKERS", defaultOutboxWorkers, &problems)
	cfg.MaxInstances = nonNegativeIntValue("WZAP_MAX_INSTANCES", 0, &problems)
	cfg.DefaultUserQuota = nonNegativeIntValue("WZAP_DEFAULT_USER_INSTANCE_QUOTA", 0, &problems)
	cfg.Humanize = boolValue("WZAP_HUMANIZE", false, &problems)
	cfg.AutoMigrate = boolValue("WZAP_AUTO_MIGRATE", defaultAutoMigrate, &problems)
	cfg.Chatwoot.Enabled = boolValue("WZAP_CHATWOOT_ENABLED", false, &problems)
	cfg.Chatwoot.BotContact = os.Getenv("WZAP_CHATWOOT_BOT_CONTACT")
	cfg.Chatwoot.MessageRead = boolValue("WZAP_CHATWOOT_MESSAGE_READ", false, &problems)
	cfg.Chatwoot.MessageDelete = boolValue("WZAP_CHATWOOT_MESSAGE_DELETE", false, &problems)
	cfg.Chatwoot.ImportDBURL = os.Getenv("WZAP_CHATWOOT_IMPORT_DB_URL")
	cfg.Chatwoot.ImportPlaceholder = boolValue("WZAP_CHATWOOT_IMPORT_PLACEHOLDER", false, &problems)

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}

	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// positiveIntValue reads a numeric variable that is only meaningful when it is
// positive, reporting malformed and non-positive values.
func positiveIntValue(name string, fallback int, problems *[]string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be an integer, got %q", name, raw))
		return fallback
	}
	if value <= 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be positive, got %q", name, raw))
		return fallback
	}
	return value
}

// nonNegativeIntValue reads a numeric variable where zero is meaningful
// (unlimited), reporting malformed and negative values.
func nonNegativeIntValue(name string, fallback int, problems *[]string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be an integer, got %q", name, raw))
		return fallback
	}
	if value < 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be non-negative, got %q", name, raw))
		return fallback
	}
	return value
}

// positiveInt64Value is positiveIntValue for 64-bit values such as byte sizes.
func positiveInt64Value(name string, fallback int64, problems *[]string) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be an integer, got %q", name, raw))
		return fallback
	}
	if value <= 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be positive, got %q", name, raw))
		return fallback
	}
	return value
}

func boolValue(name string, fallback bool, problems *[]string) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be a boolean, got %q", name, raw))
		return fallback
	}
	return value
}
