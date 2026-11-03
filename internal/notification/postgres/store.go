// Package postgres реализует хранилище уведомлений на PostgreSQL (04 §8). Запросы — в
// queries/*.sql, код доступа генерирует sqlc. Методы работают в транзакции из context.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Store — хранилище модуля; реализует domain.Repository.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ domain.Repository = (*Store)(nil)

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store { return &Store{pool: pool, log: log} }

func (s *Store) q(ctx context.Context) *sqlc.Queries { return sqlc.New(db.Conn(ctx, s.pool)) }

func (s *Store) fail(ctx context.Context, op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if !errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "repository call failed", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	return err
}

// Insert вставляет строки; строка с уже записанным ключом повтора пропускается (R-10).
func (s *Store) Insert(ctx context.Context, ns []domain.Notification) ([]domain.Notification, error) {
	out := make([]domain.Notification, 0, len(ns))
	for _, n := range ns {
		payload, err := json.Marshal(n.Payload)
		if err != nil {
			return nil, fmt.Errorf("notification payload: %w", err)
		}
		rows, err := s.q(ctx).InsertNotification(ctx, sqlc.InsertNotificationParams{
			ID: n.ID, UserID: n.UserID, OrganizationID: n.OrganizationID, Channel: string(n.Channel), Template: n.Template,
			Payload: payload, Status: string(n.Status), ObjectType: n.ObjectType, ObjectID: n.ObjectID, DedupeKey: n.DedupeKey,
			MaxUserID: n.MaxUserID, Attempts: int32(n.Attempts), LastError: n.LastError, SentAt: n.SentAt, ReadAt: n.ReadAt,
			CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		})
		if err != nil {
			return nil, s.fail(ctx, "Notification.Insert", err)
		}
		if rows == 1 {
			out = append(out, n)
		}
	}
	return out, nil
}

// GetForUpdate — уведомление под блокировкой.
func (s *Store) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	row, err := s.q(ctx).GetNotificationForUpdate(ctx, id)
	if err != nil {
		return domain.Notification{}, s.fail(ctx, "Notification.GetForUpdate", err)
	}
	return toDomain(row)
}

// Update сохраняет состояние отправки.
func (s *Store) Update(ctx context.Context, n domain.Notification) error {
	rows, err := s.q(ctx).UpdateNotification(ctx, sqlc.UpdateNotificationParams{
		ID: n.ID, Status: string(n.Status), Attempts: int32(n.Attempts), LastError: n.LastError,
		SentAt: n.SentAt, ReadAt: n.ReadAt, UpdatedAt: n.UpdatedAt,
	})
	if err != nil {
		return s.fail(ctx, "Notification.Update", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListCabinet — уведомления кабинета пользователя, новые сверху (R-12).
func (s *Store) ListCabinet(ctx context.Context, userID uuid.UUID, unread bool, after *domain.Cursor, limit int) ([]domain.Notification, error) {
	p := sqlc.ListCabinetNotificationsParams{UserID: userID, Unread: unread, Lim: int32(limit)}
	if after != nil {
		p.HasCursor, p.CursorAt, p.CursorID = true, after.CreatedAt, after.ID
	}
	rows, err := s.q(ctx).ListCabinetNotifications(ctx, p)
	if err != nil {
		return nil, s.fail(ctx, "Notification.ListCabinet", err)
	}
	out := make([]domain.Notification, 0, len(rows))
	for _, r := range rows {
		n, err := toDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// CountCabinet — число уведомлений кабинета по фильтру.
func (s *Store) CountCabinet(ctx context.Context, userID uuid.UUID, unread bool) (int, error) {
	n, err := s.q(ctx).CountCabinetNotifications(ctx, sqlc.CountCabinetNotificationsParams{UserID: userID, Unread: unread})
	if err != nil {
		return 0, s.fail(ctx, "Notification.CountCabinet", err)
	}
	return int(n), nil
}

// MarkRead отмечает своё уведомление кабинета; нет, чужое или не кабинетное — ErrNotFound.
func (s *Store) MarkRead(ctx context.Context, userID, id uuid.UUID, now time.Time) error {
	rows, err := s.q(ctx).MarkNotificationRead(ctx, sqlc.MarkNotificationReadParams{Now: now, ID: id, UserID: userID})
	if err != nil {
		return s.fail(ctx, "Notification.MarkRead", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// MarkAllRead отмечает все непрочитанные уведомления кабинета пользователя.
func (s *Store) MarkAllRead(ctx context.Context, userID uuid.UUID, now time.Time) (int64, error) {
	n, err := s.q(ctx).MarkAllNotificationsRead(ctx, sqlc.MarkAllNotificationsReadParams{Now: &now, UserID: userID})
	if err != nil {
		return 0, s.fail(ctx, "Notification.MarkAllRead", err)
	}
	return n, nil
}

// DeleteBefore удаляет пачку уведомлений в конечном состоянии старше before (R-16;
// cleanup.DeleteFunc). pending не удаляются.
func (s *Store) DeleteBefore(ctx context.Context, before time.Time, batch int32) (int64, error) {
	n, err := s.q(ctx).DeleteOldNotifications(ctx, sqlc.DeleteOldNotificationsParams{Before: before, Lim: batch})
	if err != nil {
		return 0, s.fail(ctx, "Notification.DeleteBefore", err)
	}
	return n, nil
}

func toDomain(r sqlc.Notification) (domain.Notification, error) {
	payload := map[string]any{}
	if len(r.Payload) > 0 {
		dec := json.NewDecoder(bytes.NewReader(r.Payload))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return domain.Notification{}, fmt.Errorf("notification %s payload: %w", r.ID, err)
		}
	}
	return domain.Notification{
		ID: r.ID, UserID: r.UserID, OrganizationID: r.OrganizationID, Channel: domain.Channel(r.Channel), Template: r.Template,
		Payload: payload, Status: domain.Status(r.Status), ObjectType: r.ObjectType, ObjectID: r.ObjectID, DedupeKey: r.DedupeKey,
		MaxUserID: r.MaxUserID, Attempts: int(r.Attempts), LastError: r.LastError, SentAt: utcPtr(r.SentAt), ReadAt: utcPtr(r.ReadAt),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// Collector — метрика maxim_notifications{channel,status} (07 §5.5): число уведомлений по
// каналу и состоянию, читается из базы при сборе.
type Collector struct {
	s    *Store
	desc *prometheus.Desc
}

// NewCollector создаёт сборщик.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s, desc: prometheus.NewDesc("maxim_notifications", "Notifications by channel and status.",
		[]string{"channel", "status"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.s.q(ctx).CountNotificationsByStatus(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "notification metrics query failed", logx.KeyOp, "Notification.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range rows {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(r.N), r.Channel, r.Status)
	}
}
