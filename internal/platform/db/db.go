// Package db — пул соединений PostgreSQL, доступ к текущей транзакции для
// кода sqlc и миграции.
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// DBTX — то, что принимает код sqlc: пул или транзакция.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Conn возвращает транзакцию из context или пул, если транзакции нет.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if t, ok := tx.FromContext(ctx); ok {
		return t
	}
	return pool
}

// Open создаёт пул и проверяет соединение.
func Open(ctx context.Context, url string, maxConns int32, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("parse DATABASE_URL: invalid connection string") // строка содержит пароль — не выводим
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	log.DebugContext(ctx, "db connected", logx.KeyOp, "DB.Open", "max_conns", maxConns,
		"host", cfg.ConnConfig.Host, "database", cfg.ConnConfig.Database)
	return pool, nil
}

// Ping проверяет связь с базой (для /readyz).
func Ping(pool *pgxpool.Pool) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := pool.Exec(ctx, "SELECT 1")
		return err
	}
}

// UniqueViolation сообщает, что err — нарушение уникальности constraint.
func UniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && (constraint == "" || pg.ConstraintName == constraint)
}

// CheckViolation сообщает, что err — нарушение проверки constraint (CHECK или
// триггер-ограничение с ERRCODE check_violation).
func CheckViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" && (constraint == "" || pg.ConstraintName == constraint)
}
