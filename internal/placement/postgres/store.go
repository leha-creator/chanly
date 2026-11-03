// Package postgres реализует порты-репозитории модуля размещений на PostgreSQL.
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Ограничения базы, нарушение которых — попытка нарушить инвариант (R-20).
var invariantConstraints = []string{
	"placement_terminal_guard", "placement_amounts_immutable", "placement_amounts_sum",
	"placement_schedule_pair", "placement_scheduled_has_time",
}

// Store — общий доступ к базе для репозиториев модуля.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log}
}

// Carts возвращает репозиторий корзины.
func (s *Store) Carts() *Carts { return &Carts{s} }

// Orders возвращает репозиторий заказов.
func (s *Store) Orders() *Orders { return &Orders{s} }

// Placements возвращает репозиторий размещений.
func (s *Store) Placements() *Placements { return &Placements{s} }

// History возвращает репозиторий истории состояний.
func (s *Store) History() *History { return &History{s} }

// Publications возвращает репозиторий публикаций.
func (s *Store) Publications() *Publications { return &Publications{s} }

// Violations возвращает репозиторий нарушений.
func (s *Store) Violations() *Violations { return &Violations{s} }

// Disputes возвращает репозиторий споров.
func (s *Store) Disputes() *Disputes { return &Disputes{s} }

var (
	_ domain.Carts        = (*Carts)(nil)
	_ domain.Orders       = (*Orders)(nil)
	_ domain.Placements   = (*Placements)(nil)
	_ domain.History      = (*History)(nil)
	_ domain.Publications = (*Publications)(nil)
	_ domain.Violations   = (*Violations)(nil)
	_ domain.Disputes     = (*Disputes)(nil)
)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// debug пишет вызов метода репозитория.
func (s *Store) debug(ctx context.Context, op string, args ...any) {
	s.log.DebugContext(ctx, "repository call", append([]any{logx.KeyOp, op}, args...)...)
}

// fail переводит ошибку базы: «нет строк» — domain.ErrNotFound; нарушение инварианта
// триггером или проверкой — ERROR и 500 (R-20); нарушение уникальности — 409.
// Остальное пишется в журнал как непредвиденное и возвращается как есть.
func (s *Store) fail(ctx context.Context, op string, err error, args ...any) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		for _, c := range invariantConstraints {
			if pg.Code == "23514" && pg.ConstraintName == c {
				s.log.ErrorContext(ctx, "placement invariant violated",
					append([]any{logx.KeyOp, op, "constraint", c}, args...)...)
				return apperr.Internal(err).WithDetail("reason", "invariant_violated")
			}
		}
		if db.UniqueViolation(err, "") {
			s.log.WarnContext(ctx, "unique constraint violated", append([]any{logx.KeyOp, op, "constraint", pg.ConstraintName}, args...)...)
			return apperr.Conflict("record already exists").WithDetail("constraint", pg.ConstraintName).WithCause(err)
		}
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

func pgTime(t domain.TimeOfDay) pgtype.Time {
	return pgtype.Time{Microseconds: int64(t.Duration() / time.Microsecond), Valid: true}
}

func fromPgTime(t pgtype.Time) domain.TimeOfDay {
	return domain.TimeOfDay(t.Microseconds / int64(time.Minute/time.Microsecond))
}
