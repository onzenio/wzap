package config

import (
	"errors"
	"testing"

	"wzap/internal/model"
)

func enabledConfig() model.ChatwootConfig {
	return model.ChatwootConfig{
		Enabled:   true,
		URL:       "https://chatwoot.example.com",
		AccountID: "1",
		Token:     "secret",
	}
}

func TestValidateDisabledPassesWithEmptyFields(t *testing.T) {
	if err := Validate(model.ChatwootConfig{}); err != nil {
		t.Fatalf("Validate(disabled) = %v, want nil", err)
	}
}

func TestValidateEnabledRequiresURL(t *testing.T) {
	cfg := enabledConfig()
	cfg.URL = ""
	err := Validate(cfg)
	var field *ErrField
	if !errors.As(err, &field) {
		t.Fatalf("Validate() error = %v (%T), want *ErrField", err, err)
	}
	if field.Field != "url" {
		t.Errorf("field = %q, want %q", field.Field, "url")
	}
}

func TestValidateEnabledRequiresAccountID(t *testing.T) {
	cfg := enabledConfig()
	cfg.AccountID = ""
	err := Validate(cfg)
	var field *ErrField
	if !errors.As(err, &field) {
		t.Fatalf("Validate() error = %v (%T), want *ErrField", err, err)
	}
	if field.Field != "account_id" {
		t.Errorf("field = %q, want %q", field.Field, "account_id")
	}
}

func TestValidateEnabledRequiresToken(t *testing.T) {
	cfg := enabledConfig()
	cfg.Token = ""
	err := Validate(cfg)
	var field *ErrField
	if !errors.As(err, &field) {
		t.Fatalf("Validate() error = %v (%T), want *ErrField", err, err)
	}
	if field.Field != "token" {
		t.Errorf("field = %q, want %q", field.Field, "token")
	}
}

func TestValidateRejectsMalformedURL(t *testing.T) {
	for _, raw := range []string{"://bad", "ftp://chatwoot.example.com", "http://", "not a url %%"} {
		cfg := enabledConfig()
		cfg.URL = raw
		err := Validate(cfg)
		var field *ErrField
		if !errors.As(err, &field) {
			t.Fatalf("Validate(url=%q) error = %v (%T), want *ErrField", raw, err, err)
		}
		if field.Field != "url" {
			t.Errorf("Validate(url=%q) field = %q, want %q", raw, field.Field, "url")
		}
	}
}

func TestValidateEnabledValidPasses(t *testing.T) {
	if err := Validate(enabledConfig()); err != nil {
		t.Fatalf("Validate(valid) = %v, want nil", err)
	}
}

func TestDefaults(t *testing.T) {
	if got := DefaultInbox("my-instance"); got != "my-instance" {
		t.Errorf("DefaultInbox = %q, want %q", got, "my-instance")
	}
	if got := DefaultDelimiter(); got != "\n" {
		t.Errorf("DefaultDelimiter = %q, want %q", got, "\n")
	}
}
