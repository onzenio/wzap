package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"wzap/internal/auth"
	"wzap/internal/config"
	"wzap/internal/model"
	"wzap/internal/storage"
)

// ownerBackfiller claims the legacy instances with a NULL owner. It is the
// narrow contract seedAdmin needs: the postgres instance repository satisfies
// it without widening storage.InstanceRepository.
type ownerBackfiller interface {
	BackfillOwner(ctx context.Context, owner uuid.UUID) (int64, error)
}

// seedAdmin creates the initial admin from WZAP_ADMIN_EMAIL and
// WZAP_ADMIN_PASSWORD when the users table is empty, and assigns the legacy
// ownerless instances to him. It is a no-op when users already exist (never
// duplicating the admin, never touching owners) and when both admin variables
// are unset. A half-configured seed warns, creates nothing and lets boot
// proceed.
func seedAdmin(ctx context.Context, cfg config.Config, users storage.UserRepository, instances ownerBackfiller, log *slog.Logger) error {
	count, err := users.Count(ctx)
	if err != nil {
		return fmt.Errorf("seed admin: count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	email, password := cfg.AdminEmail, cfg.AdminPassword
	switch {
	case email == "" && password == "":
		return nil
	case email == "" || password == "":
		log.Warn("admin seed skipped: set both WZAP_ADMIN_EMAIL and WZAP_ADMIN_PASSWORD to create the initial admin")
		return nil
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("seed admin: hash password: %w", err)
	}
	admin, err := users.Create(ctx, model.User{
		ID:            uuid.New(),
		Email:         email,
		PasswordHash:  hash,
		Role:          "admin",
		InstanceQuota: cfg.DefaultUserQuota,
	})
	if err != nil {
		return fmt.Errorf("seed admin: create admin: %w", err)
	}

	claimed, err := instances.BackfillOwner(ctx, admin.ID)
	if err != nil {
		return fmt.Errorf("seed admin: backfill instance owners: %w", err)
	}

	log.Info("admin seed created initial admin", "email", email, "claimed_instances", claimed)
	return nil
}
