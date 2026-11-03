// Package postgres — хранилище модуля администратора: запросы второго подтверждения
// (admin_approval) и чтение журнала действий. Запросы — queries/*.sql (sqlc).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/admin/app"
	"github.com/leha-creator/chanly/internal/admin/domain"
	"github.com/leha-creator/chanly/internal/admin/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Store — хранилище модуля.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ domain.Store = (*Store)(nil)

// New создаёт хранилище.
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

func (s *Store) Insert(ctx context.Context, a domain.Approval) error {
	err := s.q(ctx).InsertApproval(ctx, sqlc.InsertApprovalParams{ID: a.ID, Kind: string(a.Kind), Payload: a.Payload,
		Amount: a.Amount, Summary: a.Summary, Reason: a.Reason, IdempotencyKey: a.IdempotencyKey, RequestedBy: a.RequestedBy,
		Status: string(a.Status), ExpiresAt: a.ExpiresAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt})
	if err != nil {
		if db.UniqueViolation(err, "admin_approval_idempotency_key_key") {
			return domain.ErrDuplicateKey
		}
		return s.fail(ctx, "Admin.InsertApproval", err)
	}
	return nil
}

func (s *Store) ByKey(ctx context.Context, key string) (domain.Approval, error) {
	row, err := s.q(ctx).GetApprovalByKey(ctx, key)
	if err != nil {
		return domain.Approval{}, s.fail(ctx, "Admin.ApprovalByKey", err)
	}
	return approvalOf(row), nil
}

func (s *Store) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	row, err := s.q(ctx).GetApprovalForUpdate(ctx, id)
	if err != nil {
		return domain.Approval{}, s.fail(ctx, "Admin.ApprovalForUpdate", err)
	}
	return approvalOf(row), nil
}

func (s *Store) Update(ctx context.Context, a domain.Approval) error {
	var reason *string
	if a.DecisionReason != "" {
		reason = &a.DecisionReason
	}
	err := s.q(ctx).UpdateApproval(ctx, sqlc.UpdateApprovalParams{Status: string(a.Status), DecidedBy: a.DecidedBy,
		DecidedAt: a.DecidedAt, DecisionReason: reason, ResultID: a.ResultID, UpdatedAt: a.UpdatedAt, ID: a.ID})
	if err != nil {
		return s.fail(ctx, "Admin.UpdateApproval", err)
	}
	return nil
}

func (s *Store) ExpireBefore(ctx context.Context, now time.Time) ([]domain.Approval, error) {
	rows, err := s.q(ctx).ExpireApprovals(ctx, now)
	if err != nil {
		return nil, s.fail(ctx, "Admin.ExpireApprovals", err)
	}
	out := make([]domain.Approval, len(rows))
	for i, r := range rows {
		out[i] = approvalOf(r)
	}
	return out, nil
}

func (s *Store) List(ctx context.Context, status *domain.Status, p domain.Page) ([]domain.Approval, int, error) {
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	rows, err := s.q(ctx).ListApprovals(ctx, sqlc.ListApprovalsParams{Status: st, Off: int32(p.Offset), Lim: int32(p.Limit)})
	if err != nil {
		return nil, 0, s.fail(ctx, "Admin.ListApprovals", err)
	}
	total, err := s.q(ctx).CountApprovals(ctx, st)
	if err != nil {
		return nil, 0, s.fail(ctx, "Admin.CountApprovals", err)
	}
	out := make([]domain.Approval, len(rows))
	for i, r := range rows {
		out[i] = approvalOf(r)
	}
	return out, int(total), nil
}

func (s *Store) CountPending(ctx context.Context) (int, error) {
	st := string(domain.StatusPending)
	n, err := s.q(ctx).CountApprovals(ctx, &st)
	if err != nil {
		return 0, s.fail(ctx, "Admin.CountPending", err)
	}
	return int(n), nil
}

func approvalOf(r sqlc.AdminApproval) domain.Approval {
	a := domain.Approval{ID: r.ID, Kind: domain.Kind(r.Kind), Payload: r.Payload, Amount: r.Amount, Summary: r.Summary,
		Reason: r.Reason, IdempotencyKey: r.IdempotencyKey, RequestedBy: r.RequestedBy, Status: domain.Status(r.Status),
		DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt, ResultID: r.ResultID, ExpiresAt: r.ExpiresAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.DecisionReason != nil {
		a.DecisionReason = *r.DecisionReason
	}
	var obj struct {
		PayoutID *uuid.UUID `json:"payout_id"`
	}
	if json.Unmarshal(r.Payload, &obj) == nil {
		a.ObjectID = obj.PayoutID
	}
	return a
}

// Metrics — maxim_admin_approvals_total{kind,decision}.
type Metrics struct{ decisions *prometheus.CounterVec }

// NewMetrics создаёт и регистрирует метрики.
func NewMetrics(reg interface{ MustRegister(...prometheus.Collector) }) *Metrics {
	m := &Metrics{decisions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_admin_approvals_total",
		Help: "Second approval decisions by kind and decision (approved, rejected, expired)."}, []string{"kind", "decision"})}
	reg.MustRegister(m.decisions)
	return m
}

func (m *Metrics) Approval(kind domain.Kind, decision string) {
	m.decisions.WithLabelValues(string(kind), decision).Inc()
}

// AuditLog — чтение audit_log без IP-адреса (R-15).
func (s *Store) AuditLog(ctx context.Context, f app.AuditFilter, p domain.Page) ([]app.AuditEntry, int, error) {
	q := s.q(ctx)
	rows, err := q.ListAuditLog(ctx, sqlc.ListAuditLogParams{ActorUserID: f.ActorUserID, ActorType: f.ActorType,
		Action: f.Action, ObjectType: f.ObjectType, ObjectID: f.ObjectID, OrganizationID: f.OrganizationID,
		CreatedFrom: f.From, CreatedTo: f.To, Off: int32(p.Offset), Lim: int32(p.Limit)})
	if err != nil {
		return nil, 0, s.fail(ctx, "Admin.AuditLog", err)
	}
	total, err := q.CountAuditLog(ctx, sqlc.CountAuditLogParams{ActorUserID: f.ActorUserID, ActorType: f.ActorType,
		Action: f.Action, ObjectType: f.ObjectType, ObjectID: f.ObjectID, OrganizationID: f.OrganizationID,
		CreatedFrom: f.From, CreatedTo: f.To})
	if err != nil {
		return nil, 0, s.fail(ctx, "Admin.CountAuditLog", err)
	}
	out := make([]app.AuditEntry, len(rows))
	for i, r := range rows {
		out[i] = app.AuditEntry{ID: r.ID, ActorType: r.ActorType, ActorUserID: r.ActorUserID, OrganizationID: r.OrganizationID,
			Action: r.Action, ObjectType: r.ObjectType, ObjectID: r.ObjectID, Reason: r.Reason, Data: r.Data, CreatedAt: r.CreatedAt}
	}
	return out, int(total), nil
}
