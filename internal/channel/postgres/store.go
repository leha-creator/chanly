// Package postgres реализует порты-репозитории модуля каналов на PostgreSQL.
// Запросы — в queries/*.sql, код доступа генерирует sqlc. Все методы работают
// в транзакции из context, если она открыта.
package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
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

// Channels возвращает репозиторий каналов.
func (s *Store) Channels() *Channels { return &Channels{s} }

// LinkCodes возвращает репозиторий кодов подключения.
func (s *Store) LinkCodes() *LinkCodes { return &LinkCodes{s} }

// Categories возвращает справочник категорий.
func (s *Store) Categories() *Categories { return &Categories{s} }

// Formats возвращает справочник форматов.
func (s *Store) Formats() *Formats { return &Formats{s} }

// Prices возвращает репозиторий цен.
func (s *Store) Prices() *Prices { return &Prices{s} }

// Unavailabilities возвращает репозиторий интервалов недоступности.
func (s *Store) Unavailabilities() *Unavailabilities { return &Unavailabilities{s} }

// Metrics возвращает репозиторий снимков метрик.
func (s *Store) Metrics() *Metrics { return &Metrics{s} }

// Catalog возвращает выборки каталога.
func (s *Store) Catalog() *Catalog { return &Catalog{s} }

// AdminChannels возвращает выборки администратора.
func (s *Store) AdminChannels() *AdminChannels { return &AdminChannels{s} }

var (
	_ domain.Channels         = (*Channels)(nil)
	_ domain.LinkCodes        = (*LinkCodes)(nil)
	_ domain.Categories       = (*Categories)(nil)
	_ domain.Formats          = (*Formats)(nil)
	_ domain.Prices           = (*Prices)(nil)
	_ domain.Unavailabilities = (*Unavailabilities)(nil)
	_ domain.Metrics          = (*Metrics)(nil)
	_ domain.Catalog          = (*Catalog)(nil)
	_ domain.AdminChannels    = (*AdminChannels)(nil)
)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// debug пишет вызов метода репозитория. Коды и тексты сообщений сюда не передаются.
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
