// Package postgres реализует порты-репозитории модуля post на PostgreSQL.
// Запросы — в queries/*.sql, код доступа генерирует sqlc. Все методы работают
// в транзакции из context, если она открыта.
package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
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

// MediaFiles возвращает репозиторий оригиналов.
func (s *Store) MediaFiles() *MediaFiles { return &MediaFiles{s} }

// Derivatives возвращает репозиторий производных.
func (s *Store) Derivatives() *Derivatives { return &Derivatives{s} }

// Campaigns возвращает репозиторий кампаний.
func (s *Store) Campaigns() *Campaigns { return &Campaigns{s} }

// Posts возвращает репозиторий постов.
func (s *Store) Posts() *Posts { return &Posts{s} }

// Versions возвращает репозиторий версий поста.
func (s *Store) Versions() *Versions { return &Versions{s} }

var (
	_ domain.MediaFiles  = (*MediaFiles)(nil)
	_ domain.Derivatives = (*Derivatives)(nil)
	_ domain.Campaigns   = (*Campaigns)(nil)
	_ domain.Posts       = (*Posts)(nil)
	_ domain.Versions    = (*Versions)(nil)
)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// debug пишет вызов метода репозитория. Текст поста и имена файлов сюда не передаются.
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

// cursorArgs раскладывает курсор на необязательные параметры запроса.
func cursorArgs(c *domain.PageCursor) (*time.Time, *uuid.UUID) {
	if c == nil {
		return nil, nil
	}
	t, id := c.CreatedAt, c.ID
	return &t, &id
}

// text — пустая строка домена хранится как NULL.
func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i32(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func i64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
