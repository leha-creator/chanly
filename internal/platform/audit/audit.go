// Package audit — журнал действий (04 §8): кто, что и над чем сделал, значения
// до и после. Запись идёт в транзакции из context — вместе с самим действием.
// Таблица неизменяема: роль приложения может только добавлять строки.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/platform/audit/sqlc"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Типы действующего лица (04 §9, actor_type).
const (
	ActorAdvertiser = "advertiser"
	ActorBlogger    = "blogger"
	ActorAdmin      = "admin"
	ActorSystem     = "system"
)

// Actor — кто действует. Кладётся в context слоем HTTP или командой.
type Actor struct {
	Type           string
	UserID         *uuid.UUID
	OrganizationID *uuid.UUID
	IP             *netip.Addr
}

type actorKey struct{}

// WithActor кладёт действующее лицо в context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom возвращает действующее лицо; без него — system.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Type: ActorSystem}
}

// Entry — запись журнала действий.
type Entry struct {
	Action     string // например organization.update
	ObjectType string
	ObjectID   string
	Reason     *string
	// Data — что изменилось; сериализуется в JSON. Персональные данные — через Mask.
	Data any
	// ActorType заменяет тип из context: например, кабинет, в котором действует пользователь.
	ActorType string
	// OrganizationID заменяет организацию из context: например, только что созданную.
	OrganizationID *uuid.UUID
}

// Recorder пишет журнал действий. Слой app зависит только от этого интерфейса.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
}

// Log — журнал действий в таблице audit_log.
type Log struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	log   *slog.Logger
}

// New создаёт журнал действий.
func New(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Log {
	return &Log{pool: pool, clock: clk, log: log}
}

// Record добавляет запись в транзакции из context.
func (l *Log) Record(ctx context.Context, e Entry) error {
	a := ActorFrom(ctx)
	if e.ActorType != "" {
		a.Type = e.ActorType
	}
	if e.OrganizationID != nil {
		a.OrganizationID = e.OrganizationID
	}
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("audit %s: marshal data: %w", e.Action, err)
	}
	id := ids.New()
	err = sqlc.New(db.Conn(ctx, l.pool)).InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		ID: id, ActorType: a.Type, ActorUserID: a.UserID, OrganizationID: a.OrganizationID,
		Action: e.Action, ObjectType: e.ObjectType, ObjectID: e.ObjectID, Reason: e.Reason,
		Data: raw, Ip: a.IP, CreatedAt: l.clock.Now(),
	})
	if err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	l.log.DebugContext(ctx, "audit recorded", logx.KeyOp, "Audit.Record", "action", e.Action,
		"object_type", e.ObjectType, "object_id", e.ObjectID, "actor_type", a.Type)
	return nil
}

// Маскирование персональных данных в данных журнала действий (04 §10, 07 §5.4).
var maskers = map[string]func(string) string{
	"inn":               logx.MaskINN,
	"phone":             logx.MaskPhone,
	"email":             logx.MaskEmail,
	"name":              logx.MaskName,
	"contact_name":      logx.MaskName,
	"signer_name":       logx.MaskName,
	"blogger_full_name": logx.MaskName,
	"bank_account":      logx.MaskINN,
	"bank_corr_account": logx.MaskINN,
}

// Mask возвращает копию values, где персональные данные замаскированы.
func Mask(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		if s, ok := v.(string); ok {
			if m, ok := maskers[k]; ok {
				v = m(s)
			}
		}
		out[k] = v
	}
	return out
}

// Diff — данные записи «до и после» только по изменённым полям, с маскированием.
func Diff(before, after map[string]any, changed []string) map[string]any {
	b, a := map[string]any{}, map[string]any{}
	for _, k := range changed {
		if v, ok := before[k]; ok {
			b[k] = v
		}
		if v, ok := after[k]; ok {
			a[k] = v
		}
	}
	return map[string]any{"before": Mask(b), "after": Mask(a)}
}

// CountByUserSince — сколько раз пользователь выполнил действие начиная с since.
// Нужен лимитам частоты, общим для всех экземпляров api (07 §4).
func (l *Log) CountByUserSince(ctx context.Context, action string, userID uuid.UUID, since time.Time) (int, error) {
	n, err := sqlc.New(db.Conn(ctx, l.pool)).CountAuditByActorSince(ctx, sqlc.CountAuditByActorSinceParams{
		Action: action, ActorUserID: &userID, Since: since,
	})
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", action, err)
	}
	return int(n), nil
}

// CountByIPSince — сколько раз действие выполнено с адреса ip начиная с since (07 §4).
func (l *Log) CountByIPSince(ctx context.Context, action string, ip netip.Addr, since time.Time) (int, error) {
	n, err := sqlc.New(db.Conn(ctx, l.pool)).CountAuditByIPSince(ctx, sqlc.CountAuditByIPSinceParams{
		Action: action, Ip: &ip, Since: since,
	})
	if err != nil {
		return 0, fmt.Errorf("count %s by ip: %w", action, err)
	}
	return int(n), nil
}
