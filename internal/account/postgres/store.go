// Package postgres реализует порты-репозитории модуля аккаунтов на PostgreSQL.
// Запросы — в queries/*.sql, код доступа генерирует sqlc. Все методы работают
// в транзакции из context, если она открыта.
package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Store — общий доступ к базе для репозиториев модуля.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log}
}

// Users возвращает репозиторий пользователей.
func (s *Store) Users() *Users { return &Users{s} }

// AuthCodes возвращает репозиторий кодов входа.
func (s *Store) AuthCodes() *AuthCodes { return &AuthCodes{s} }

// Sessions возвращает репозиторий сессий.
func (s *Store) Sessions() *Sessions { return &Sessions{s} }

// Organizations возвращает репозиторий организаций.
func (s *Store) Organizations() *Organizations { return &Organizations{s} }

// LegalDocuments возвращает репозиторий оферт.
func (s *Store) LegalDocuments() *LegalDocuments { return &LegalDocuments{s} }

var (
	_ domain.Users          = (*Users)(nil)
	_ domain.AuthCodes      = (*AuthCodes)(nil)
	_ domain.Sessions       = (*Sessions)(nil)
	_ domain.Organizations  = (*Organizations)(nil)
	_ domain.LegalDocuments = (*LegalDocuments)(nil)
)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// debug пишет вызов метода репозитория. Почта, ИНН и токены сюда не передаются.
func (s *Store) debug(ctx context.Context, op string, args ...any) {
	s.log.DebugContext(ctx, "repository call", append([]any{logx.KeyOp, op}, args...)...)
}

// fail переводит ошибку базы: «нет строк» — domain.ErrNotFound, остальное
// пишется в журнал как непредвиденное и возвращается как есть.
func (s *Store) fail(ctx context.Context, op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if !errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "repository call failed", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	return err
}

func ptr[T any](v T) *T { return &v }

func strPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func enumPtr[T ~string](v *string) *T {
	if v == nil {
		return nil
	}
	e := T(*v)
	return &e
}
