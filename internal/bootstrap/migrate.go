package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"github.com/leha-creator/chanly/internal/platform/db"
)

// runMigrate — режим migrate: up, down (одна миграция) или status.
// DATABASE_URL здесь — учётная запись владельца схемы, а не роли приложения;
// DB_APP_ROLE — роль, которой миграции выдают права.
func runMigrate(ctx context.Context, cfg Config, log *slog.Logger, args []string) error {
	if len(args) != 1 {
		return errUsage
	}
	err := db.MigrateURL(ctx, cfg.DatabaseURL, cfg.DBAppRole, args[0], log)
	if errors.Is(err, db.ErrUnknownMigrateCommand) {
		return errUsage
	}
	return err
}
