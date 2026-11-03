// Package app — сценарии журнала проводок (06 §2): проводка транзакции, удержания по
// размещениям, остатки, проверка сходимости. Журнал работает только в транзакции
// вызывающего (I-10, R-5); правила — в domain.
package app

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Поля журнала (06 §11, 07 §5.1).
const (
	KeyKind          = "ledger_kind"
	KeyTransactionID = "ledger_transaction_id"
	KeyIdempotency   = "idempotency_key"
	KeyPlacementID   = "placement_id"
	KeyAmount        = "amount"
)

// Alerter — оповещения администратору (07 §3).
type Alerter interface {
	Raise(ctx context.Context, a alert.Alert) error
	Resolve(ctx context.Context, reason, objectKey string) error
}

// Observer — метрики журнала (07 §5.5).
type Observer interface {
	// Posted — транзакция вида kind проведена.
	Posted(kind domain.Kind)
	// CheckFailed — проверка сходимости check нашла расхождение.
	CheckFailed(check string)
}

// NopObserver — метрики не собираются.
type NopObserver struct{}

func (NopObserver) Posted(domain.Kind) {}
func (NopObserver) CheckFailed(string) {}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	Store   domain.Store
	Reports domain.Reports
	// References — номера заказов и счетов для описаний операций (R-9).
	References References
	Tx         tx.Runner
	Alerts     Alerter
	Audit      audit.Recorder
	Metrics    Observer
	Clock      clock.Clock
	Log        *slog.Logger
	// Admin — журнал денег администратора (adm/ledger); nil — только команды.
	Admin domain.AdminJournal
	// ManualSecondApproval — сумма ручной проводки, выше которой нужен второй администратор (R-14).
	ManualSecondApproval money.Kopecks
}

// Service — журнал проводок.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	if d.Metrics == nil {
		d.Metrics = NopObserver{}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "ledger")}
}

// raise поднимает оповещение; ошибка оповещения не отменяет сценарий — она в журнале.
func (s *Service) raise(ctx context.Context, op string, a alert.Alert) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Raise(ctx, a); err != nil {
		s.log.ErrorContext(ctx, "alert raise failed", logx.KeyOp, op, "reason", a.Reason, logx.KeyError, err.Error())
	}
}

// resolve закрывает оповещение после восстановления.
func (s *Service) resolve(ctx context.Context, op, reason, objectKey string) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Resolve(ctx, reason, objectKey); err != nil {
		s.log.ErrorContext(ctx, "alert resolve failed", logx.KeyOp, op, "reason", reason, logx.KeyError, err.Error())
	}
}

// postingsForLog — проводки для журнала: виды счетов и суммы в копейках, без разрезов.
func postingsForLog(t domain.Transaction) []string {
	out := make([]string, 0, len(t.Postings))
	for _, p := range t.Postings {
		out = append(out, string(p.Debit.Kind)+">"+string(p.Credit.Kind)+":"+strconv.FormatInt(int64(p.Amount), 10))
	}
	return out
}
