// Package postgres реализует хранилище модуля расчётов на PostgreSQL: счета, выписки,
// поступления и закрывающие документы. Запросы — в queries/*.sql, код доступа генерирует
// sqlc. Методы работают в транзакции из context.
package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Store — хранилище модуля.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log}
}

// Invoices — репозиторий счетов.
func (s *Store) Invoices() domain.Invoices { return invoices{s} }

// Statements — репозиторий выписок.
func (s *Store) Statements() domain.Statements { return statements{s} }

// BankTransactions — репозиторий поступлений.
func (s *Store) BankTransactions() domain.BankTransactions { return bankTransactions{s} }

// ClosingActs — репозиторий закрывающих документов.
func (s *Store) ClosingActs() domain.ClosingActs { return closingActs{s} }

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// fail переводит ошибку базы: «нет строк» — domain.ErrNotFound, повтор файла выписки —
// domain.ErrStatementImported; остальное пишется в журнал как непредвиденное.
func (s *Store) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case db.UniqueViolation(err, "bank_statement_file_sha256_key"):
		return domain.ErrStatementImported
	}
	if !errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "repository call failed", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	return err
}

func cursorArgs(after *domain.Cursor) (bool, time.Time, uuid.UUID) {
	if after == nil {
		return false, time.Time{}, uuid.Nil
	}
	return true, after.At, after.ID
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidOf(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func kopecksPtr(k *money.Kopecks) *int64 {
	if k == nil {
		return nil
	}
	v := int64(*k)
	return &v
}

func kopecksOf(v *int64) *money.Kopecks {
	if v == nil {
		return nil
	}
	k := money.Kopecks(*v)
	return &k
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func pgDatePtr(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgDate(*t)
}

func dateOf(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := d.Time.UTC()
	return &t
}
