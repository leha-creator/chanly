// Package app — сценарии модуля администратора: второе подтверждение ручных денежных операций
// (R-2), журнал действий и счётчики меню (R-15, R-16).
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/admin/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Действия журнала действий.
const (
	ActionApprovalRequested = "admin_approval.requested"
	ActionApprovalApproved  = "admin_approval.approved"
	ActionApprovalRejected  = "admin_approval.rejected"
)

// Metrics — maxim_admin_approvals_total{kind,decision}.
type Metrics interface {
	Approval(kind domain.Kind, decision string)
}

// NopMetrics — без метрик (команды, тесты).
type NopMetrics struct{}

func (NopMetrics) Approval(domain.Kind, string) {}

// Deps — зависимости сервиса.
type Deps struct {
	Store     domain.Store
	Executors map[domain.Kind]domain.Executor
	Audit     audit.Recorder
	Tx        tx.Runner
	Clock     clock.Clock
	Log       *slog.Logger
	Metrics   Metrics
	// TTL — срок запроса (ADMIN_APPROVAL_TTL, 72 ч).
	TTL time.Duration
	// Audits — чтение журнала действий (задача 14); Counters — счётчики меню.
	Audits   AuditReader
	Counters []Counter
}

// Service — сценарии модуля.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	if d.Metrics == nil {
		d.Metrics = NopMetrics{}
	}
	if d.Executors == nil {
		d.Executors = map[domain.Kind]domain.Executor{}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "admin")}
}

// SetExecutor подключает исполнителя вида (переходники bootstrap/bridge).
func (s *Service) SetExecutor(k domain.Kind, e domain.Executor) { s.Executors[k] = e }

func adminActor(adminID uuid.UUID) audit.Actor {
	return audit.Actor{Type: audit.ActorAdmin, UserID: &adminID}
}

// RequestApproval создаёт запрос подтверждения; повтор ключа возвращает тот же запрос
// (created = false). Исполнения нет: его сделает Approve другого senior.
func (s *Service) RequestApproval(ctx context.Context, r domain.Request) (a domain.Approval, created bool, err error) {
	const op = "Admin.RequestApproval"
	now := s.Clock.Now()
	a, err = domain.NewApproval(ids.New(), r, s.TTL, now)
	if err != nil {
		return domain.Approval{}, false, apperr.Validation("reason", "invalid").WithCause(err)
	}
	ctx = audit.WithActor(ctx, adminActor(r.AdminID))
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		prev, err := s.Store.ByKey(ctx, a.IdempotencyKey)
		switch {
		case err == nil:
			// Повтор ключа — тот же запрос; другой запрос под тем же ключом — отказ, а не подмена.
			if !sameRequest(prev, a) {
				s.log.WarnContext(ctx, "admin approval key reused", logx.KeyOp, op, "approval_id", prev.ID, "admin_user_id", r.AdminID)
				return apperr.Conflict("idempotency key reused for another request").WithDetail("reason", "idempotency_conflict")
			}
			a = prev
			return nil
		case !errors.Is(err, domain.ErrNotFound):
			return err
		}
		if err := s.Store.Insert(ctx, a); err != nil {
			if errors.Is(err, domain.ErrDuplicateKey) {
				// Одновременный запрос с тем же ключом: повторить — получить его.
				return apperr.Conflict("request with this idempotency key is in progress").WithDetail("reason", "idempotency_conflict")
			}
			return err
		}
		created = true
		return s.Audit.Record(ctx, audit.Entry{Action: ActionApprovalRequested, ObjectType: "admin_approval",
			ObjectID: a.ID.String(), Reason: &a.Reason, ActorType: audit.ActorAdmin,
			Data: map[string]any{"kind": string(a.Kind), "amount": a.Amount, "summary": a.Summary}})
	})
	if err != nil {
		return domain.Approval{}, false, err
	}
	if created {
		s.log.WarnContext(ctx, "admin approval requested", logx.KeyOp, op, "approval_id", a.ID, "kind", a.Kind,
			"amount", a.Amount, "admin_user_id", r.AdminID)
	}
	return a, created, nil
}

// List — запросы по состоянию (по умолчанию pending); просроченные сначала переводятся в expired.
func (s *Service) List(ctx context.Context, status *domain.Status, p domain.Page) ([]domain.Approval, int, error) {
	if err := s.expire(ctx); err != nil {
		return nil, 0, err
	}
	if status == nil {
		st := domain.StatusPending
		status = &st
	}
	return s.Store.List(ctx, status, p)
}

// PendingCount — запросы в pending (счётчик меню).
func (s *Service) PendingCount(ctx context.Context) (int, error) {
	if err := s.expire(ctx); err != nil {
		return 0, err
	}
	return s.Store.CountPending(ctx)
}

func (s *Service) expire(ctx context.Context) error {
	var expired []domain.Approval
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		expired, err = s.Store.ExpireBefore(ctx, s.Clock.Now())
		return err
	})
	if err != nil {
		return err
	}
	for _, a := range expired {
		s.Metrics.Approval(a.Kind, string(domain.StatusExpired))
		s.log.InfoContext(ctx, "admin approval expired", logx.KeyOp, "Admin.Expire", "approval_id", a.ID, "kind", a.Kind,
			"amount", a.Amount)
	}
	return nil
}

// Approve — подтверждение другим senior: блокировка запроса, проверка, исполнение владельцем
// операции и решение — в одной транзакции. Сбой исполнения откатывает всё: запрос остаётся pending.
func (s *Service) Approve(ctx context.Context, id, approverID uuid.UUID) (domain.Approval, error) {
	const op = "Admin.Approve"
	ctx = audit.WithActor(ctx, adminActor(approverID))
	var (
		a       domain.Approval
		expired bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if a, err = s.lock(ctx, id); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err := a.CanApprove(approverID, now); err != nil {
			if errors.Is(err, domain.ErrApprovalExpired) && a.Expire(now) {
				expired = true
				return s.Store.Update(ctx, a)
			}
			return s.refuse(ctx, op, a, approverID, err)
		}
		exec, ok := s.Executors[a.Kind]
		if !ok {
			return errors.Join(domain.ErrNoExecutor, errors.New(string(a.Kind)))
		}
		resultID, err := exec.Execute(ctx, a, approverID)
		if err != nil {
			return err
		}
		if err := a.Approve(approverID, resultID, now); err != nil {
			return err
		}
		if err := s.Store.Update(ctx, a); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: ActionApprovalApproved, ObjectType: "admin_approval",
			ObjectID: a.ID.String(), ActorType: audit.ActorAdmin, Data: map[string]any{"kind": string(a.Kind),
				"amount": a.Amount, "requested_by": a.RequestedBy.String(), "result_id": resultID.String()}})
	})
	if err != nil {
		return domain.Approval{}, err
	}
	if expired {
		s.Metrics.Approval(a.Kind, string(domain.StatusExpired))
		s.log.InfoContext(ctx, "admin approval expired", logx.KeyOp, op, "approval_id", a.ID, "kind", a.Kind, "amount", a.Amount)
		return domain.Approval{}, conflict(domain.ErrApprovalExpired)
	}
	s.Metrics.Approval(a.Kind, "approved")
	s.log.WarnContext(ctx, "admin approval approved", logx.KeyOp, op, "approval_id", a.ID, "kind", a.Kind, "amount", a.Amount,
		"admin_user_id", approverID, "requested_by", a.RequestedBy, "result_id", a.ResultID)
	return a, nil
}

// Reject — отклонение с основанием; автор может отозвать свой запрос.
func (s *Service) Reject(ctx context.Context, id, adminID uuid.UUID, reason string) (domain.Approval, error) {
	const op = "Admin.Reject"
	ctx = audit.WithActor(ctx, adminActor(adminID))
	var (
		a       domain.Approval
		expired bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if a, err = s.lock(ctx, id); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err := a.Reject(adminID, reason, now); err != nil {
			if errors.Is(err, domain.ErrInvalidApproval) {
				return apperr.Validation("reason", "required")
			}
			if errors.Is(err, domain.ErrApprovalExpired) && a.Expire(now) {
				expired = true
				return s.Store.Update(ctx, a)
			}
			return s.refuse(ctx, op, a, adminID, err)
		}
		if err := s.Store.Update(ctx, a); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: ActionApprovalRejected, ObjectType: "admin_approval",
			ObjectID: a.ID.String(), Reason: &a.DecisionReason, ActorType: audit.ActorAdmin,
			Data: map[string]any{"kind": string(a.Kind), "amount": a.Amount, "withdrawn": a.RequestedBy == adminID}})
	})
	if err != nil {
		return domain.Approval{}, err
	}
	if expired {
		s.Metrics.Approval(a.Kind, string(domain.StatusExpired))
		return domain.Approval{}, conflict(domain.ErrApprovalExpired)
	}
	s.Metrics.Approval(a.Kind, "rejected")
	s.log.WarnContext(ctx, "admin approval rejected", logx.KeyOp, op, "approval_id", a.ID, "kind", a.Kind, "amount", a.Amount,
		"admin_user_id", adminID, "withdrawn", a.RequestedBy == adminID)
	return a, nil
}

func (s *Service) lock(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	a, err := s.Store.GetForUpdate(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Approval{}, apperr.NotFound("approval not found")
	}
	return a, err
}

func (s *Service) refuse(ctx context.Context, op string, a domain.Approval, adminID uuid.UUID, err error) error {
	s.log.WarnContext(ctx, "admin approval refused", logx.KeyOp, op, "approval_id", a.ID, "status", a.Status,
		"admin_user_id", adminID, "reason", reasonOf(err))
	return conflict(err)
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, domain.ErrSameAdmin):
		return "same_admin"
	case errors.Is(err, domain.ErrApprovalExpired):
		return "approval_expired"
	default:
		return "approval_decided"
	}
}

func conflict(err error) error {
	return apperr.Conflict("approval cannot be decided").WithDetail("reason", reasonOf(err)).WithCause(err)
}

// sameRequest — повтор ключа описывает ту же операцию: вид, сумма, автор и содержимое.
func sameRequest(prev, next domain.Approval) bool {
	if prev.Kind != next.Kind || prev.Amount != next.Amount || prev.RequestedBy != next.RequestedBy {
		return false
	}
	var a, b any
	if json.Unmarshal(prev.Payload, &a) != nil || json.Unmarshal(next.Payload, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
