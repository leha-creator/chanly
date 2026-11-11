package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateURL открывает отдельный пул владельца схемы и выполняет команду миграций.
// Имя роли приложения передаётся параметром сессии maxim.app_role: его читает
// миграция прав (00002_app_role_grants.sql).
func MigrateURL(ctx context.Context, url, appRole, command string, log *slog.Logger) error {
	return MigrateURLWithControlRoles(ctx, url, appRole, command, log, ControlRoles{})
}

type ControlRoles struct{ Policy, Runner string }

func MigrateURLWithControlRoles(ctx context.Context, url, appRole, command string, log *slog.Logger, roles ControlRoles) error {
	if roles.Policy == appRole || roles.Runner == appRole || (roles.Policy != "" && roles.Policy == roles.Runner) {
		return errors.New("control roles must be distinct from app role and each other")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return errors.New("parse DATABASE_URL: invalid connection string")
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.RuntimeParams["maxim.app_role"] = appRole
	if roles.Policy != "" {
		cfg.ConnConfig.RuntimeParams["maxim.rw_policy_role"] = roles.Policy
	}
	if roles.Runner != "" {
		cfg.ConnConfig.RuntimeParams["maxim.rw_runner_role"] = roles.Runner
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()
	return migratePool(ctx, pool, command, log)
}
