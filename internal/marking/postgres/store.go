// Package postgres реализует порты-репозитории модуля маркировки на PostgreSQL.
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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
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

// Organizations возвращает репозиторий контрагентов.
func (s *Store) Organizations() *Organizations { return &Organizations{s} }

// Contracts возвращает репозиторий договоров.
func (s *Store) Contracts() *Contracts { return &Contracts{s} }

// Platforms возвращает репозиторий площадок.
func (s *Store) Platforms() *Platforms { return &Platforms{s} }

// Creatives возвращает репозиторий креативов.
func (s *Store) Creatives() *Creatives { return &Creatives{s} }

// Overview возвращает выборки по всем видам объектов.
func (s *Store) Overview() *Overview { return &Overview{s} }

var (
	_ domain.OrdOrganizations = (*Organizations)(nil)
	_ domain.OrdContracts     = (*Contracts)(nil)
	_ domain.OrdPlatforms     = (*Platforms)(nil)
	_ domain.Creatives        = (*Creatives)(nil)
	_ domain.Overview         = (*Overview)(nil)
)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// saved пишет сохранение объекта ОРД (DEBUG ord object saved).
func (s *Store) saved(ctx context.Context, op string, t domain.ObjectType, id uuid.UUID, st domain.SyncStatus) {
	s.log.DebugContext(ctx, "ord object saved", logx.KeyOp, op, "ord_object_type", t, "ord_object_id", id, "sync_status", st)
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

// syncCols — поля синхронизации в виде колонок.
type syncCols struct {
	SubmissionUncertain                                  bool
	ExternalID, RequestID, Error, PayloadHash, OrdStatus *string
	Status                                               string
	Attempts                                             int32
	SyncedAt, SentAt                                     *time.Time
}

func toCols(s domain.SyncState) syncCols {
	return syncCols{
		SubmissionUncertain: s.SubmissionUncertain,
		ExternalID:          text(s.ExternalID), RequestID: text(s.RequestID), Error: text(s.Error),
		PayloadHash: text(s.PayloadHash), OrdStatus: text(s.OrdStatus), Status: string(s.Status),
		Attempts: int32(s.Attempts), SyncedAt: tsPtr(s.SyncedAt), SentAt: tsPtr(s.SentAt),
	}
}

func fromCols(c syncCols) domain.SyncState {
	return domain.SyncState{
		SubmissionUncertain: c.SubmissionUncertain,
		Status:              domain.SyncStatus(c.Status), ExternalID: str(c.ExternalID), RequestID: str(c.RequestID),
		OrdStatus: str(c.OrdStatus), Error: str(c.Error), Attempts: int(c.Attempts),
		SentAt: ts(c.SentAt), SyncedAt: ts(c.SyncedAt), PayloadHash: str(c.PayloadHash),
	}
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

// tsPtr — нулевое время хранится как NULL.
func tsPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func ts(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.UTC()
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidVal(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

func pgDate(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func dateVal(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return time.Date(d.Time.Year(), d.Time.Month(), d.Time.Day(), 0, 0, 0, 0, time.UTC)
}
