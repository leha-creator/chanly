package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/migrations"
)

// Команды миграций.
const (
	MigrateUp     = "up"
	MigrateDown   = "down"
	MigrateStatus = "status"
)

// ErrUnknownMigrateCommand — команда не up, down или status.
var ErrUnknownMigrateCommand = errors.New("unknown migrate command")

// migratePool выполняет команду миграций на пуле владельца схемы. Пул должен нести
// параметр сессии maxim.app_role (см. MigrateURL).
// Status пишет состояние каждой миграции в журнал.
func migratePool(ctx context.Context, pool *pgxpool.Pool, command string, log *slog.Logger) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	return migrateDB(ctx, sqlDB, command, log)
}

func migrateDB(ctx context.Context, sqlDB *sql.DB, command string, log *slog.Logger) error {
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	from, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	var results []*goose.MigrationResult
	switch command {
	case MigrateUp:
		results, err = p.Up(ctx)
	case MigrateDown:
		var r *goose.MigrationResult
		if r, err = p.Down(ctx); r != nil {
			results = append(results, r)
		}
	case MigrateStatus:
		st, err := p.Status(ctx)
		if err != nil {
			return fmt.Errorf("migration status: %w", err)
		}
		for _, s := range st {
			log.InfoContext(ctx, "migration status", logx.KeyOp, "DB.Migrate", "version", s.Source.Version,
				"file", s.Source.Path, "state", string(s.State))
		}
		return nil
	default:
		return fmt.Errorf("%w %q", ErrUnknownMigrateCommand, command)
	}
	if err != nil {
		var pe *goose.PartialError
		if errors.As(err, &pe) && pe.Failed != nil {
			log.ErrorContext(ctx, "migration failed", logx.KeyOp, "DB.Migrate",
				"version", pe.Failed.Source.Version, logx.KeyError, strings.TrimSpace(pe.Err.Error()))
		}
		return fmt.Errorf("migrate %s: %w", command, err)
	}
	to, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	log.InfoContext(ctx, "migrations applied", logx.KeyOp, "DB.Migrate", "command", command,
		"from", from, "to", to, "count", len(results))
	return nil
}
