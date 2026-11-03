package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	adminapp "github.com/leha-creator/chanly/internal/admin/app"
	admindomain "github.com/leha-creator/chanly/internal/admin/domain"
	adminhttp "github.com/leha-creator/chanly/internal/admin/httpapi"
	"github.com/leha-creator/chanly/internal/apigen"
	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	ledgerhttp "github.com/leha-creator/chanly/internal/ledger/httpapi"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	markingdomain "github.com/leha-creator/chanly/internal/marking/domain"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	payouthttp "github.com/leha-creator/chanly/internal/payout/httpapi"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Переходники модуля admin (этап «API администратора», R-2, R-3, R-16): запросы второго
// подтверждения из операций ledger и payout, исполнители подтверждённых операций, счётчики меню.

// LedgerApprovals — порт ledger/httpapi.Approvals над модулем admin.
type LedgerApprovals struct{ H *adminhttp.Handlers }

func (a LedgerApprovals) RequestApproval(ctx context.Context, p *httpx.Principal, r ledgerhttp.ApprovalRequest) (apigen.AdminApproval, error) {
	return a.H.RequestApproval(ctx, p, admindomain.Request{Kind: admindomain.Kind(r.Kind), Key: r.Key, Payload: r.Payload,
		Amount: r.Amount, Summary: r.Summary, Reason: r.Reason})
}

// PayoutApprovals — порт payout/httpapi.Approvals над модулем admin.
type PayoutApprovals struct{ H *adminhttp.Handlers }

func (a PayoutApprovals) RequestApproval(ctx context.Context, p *httpx.Principal, r payouthttp.ApprovalRequest) (apigen.AdminApproval, error) {
	obj := r.ObjectID
	return a.H.RequestApproval(ctx, p, admindomain.Request{Kind: admindomain.Kind(r.Kind), Key: r.Key, Payload: r.Payload,
		Amount: r.Amount, Summary: r.Summary, Reason: r.Reason, ObjectID: &obj})
}

// executionError — отказ исполнения подтверждённой операции в ответ API (запрос остаётся pending).
func executionError(err error) error {
	var neg *ledgerdomain.NegativeLiabilityError
	switch {
	case errors.As(err, &neg):
		return apperr.Conflict("insufficient balance").WithDetail("reason", "insufficient_balance").WithCause(err)
	case errors.Is(err, ledgerdomain.ErrIdempotencyConflict):
		return apperr.Conflict("idempotency key reused").WithDetail("reason", "idempotency_conflict").WithCause(err)
	case errors.Is(err, payoutdomain.ErrInvalidPayoutState):
		return apperr.Conflict("payout state does not allow the operation").WithDetail("reason", "invalid_state").WithCause(err)
	case errors.Is(err, payoutdomain.ErrNotFound):
		return apperr.NotFound("payout not found").WithCause(err)
	}
	return err
}

// AdminLedgerManual исполняет ledger_manual: ручная проводка с ApproverID.
type AdminLedgerManual struct{ Svc *ledgerapp.Service }

func (e AdminLedgerManual) Execute(ctx context.Context, a admindomain.Approval, approverID uuid.UUID) (uuid.UUID, error) {
	var p ledgerapp.ManualPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return uuid.Nil, fmt.Errorf("approval %s payload: %w", a.ID, err)
	}
	res, err := e.Svc.Manual(ctx, ledgerapp.ManualFromPayload(a.IdempotencyKey, p, a.RequestedBy, approverID))
	if err != nil {
		return uuid.Nil, executionError(err)
	}
	return res.ID, nil
}

// AdminDepositTopup исполняет deposit_topup: bank → rocketwork_deposit с ApproverID.
type AdminDepositTopup struct{ Svc *ledgerapp.Service }

func (e AdminDepositTopup) Execute(ctx context.Context, a admindomain.Approval, approverID uuid.UUID) (uuid.UUID, error) {
	var p ledgerapp.TopupPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return uuid.Nil, fmt.Errorf("approval %s payload: %w", a.ID, err)
	}
	res, err := e.Svc.DepositTopup(ctx, a.IdempotencyKey, p.Amount, a.RequestedBy, approverID, p.Reason)
	if err != nil {
		return uuid.Nil, executionError(err)
	}
	return res.ID, nil
}

// AdminPayoutMarkPaid исполняет payout_mark_paid: отметка выплаты юрлицу со вторым senior.
type AdminPayoutMarkPaid struct{ Svc *payoutapp.Service }

func (e AdminPayoutMarkPaid) Execute(ctx context.Context, a admindomain.Approval, approverID uuid.UUID) (uuid.UUID, error) {
	var p payoutapp.MarkPaidPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return uuid.Nil, fmt.Errorf("approval %s payload: %w", a.ID, err)
	}
	paid, err := e.Svc.MarkLegalPaid(ctx, p.PayoutID, a.RequestedBy, approverID, p.BankReference, p.Reason)
	if err != nil {
		return uuid.Nil, executionError(err)
	}
	return paid.ID, nil
}

// LedgerBankStatement — порт ledger/httpapi.BankStatement над billing: остаток последней выписки.
type LedgerBankStatement struct{ Svc *billingapp.Service }

func (b LedgerBankStatement) LatestClosing(ctx context.Context) (*money.Kopecks, *time.Time, error) {
	pos, err := b.Svc.BankPosition(ctx)
	if err != nil || pos.Latest == nil {
		return nil, nil, err
	}
	return pos.Latest.Closing, pos.Latest.DateTo, nil
}

// ---------------------------------------------------------------- счётчики меню (R-16)

// CounterFunc — счётчик модуля для admin/app.Counters.
type CounterFunc struct {
	Label string
	Fn    func(ctx context.Context, c *adminapp.Counters) error
}

func (f CounterFunc) Name() string                                          { return f.Label }
func (f CounterFunc) Count(ctx context.Context, c *adminapp.Counters) error { return f.Fn(ctx, c) }

// PlacementCounter — заказы на модерации, открытые и просроченные споры.
func PlacementCounter(svc *placementapp.Service) CounterFunc {
	return CounterFunc{Label: "placement", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		n, err := svc.AdminCounts(ctx)
		if err != nil {
			return err
		}
		c.OrdersModeration, c.DisputesOpen, c.DisputesOverdue = n.OrdersModeration, n.DisputesOpen, n.DisputesOverdue
		return nil
	}}
}

// ChannelCounter — каналы на модерации.
func ChannelCounter(svc *channelapp.Service) CounterFunc {
	return CounterFunc{Label: "channel", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		n, err := svc.ModerationCount(ctx)
		c.ChannelsModeration = n
		return err
	}}
}

// BillingCounter — несопоставленные поступления.
func BillingCounter(svc *billingapp.Service) CounterFunc {
	return CounterFunc{Label: "billing", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		pos, err := svc.BankPosition(ctx)
		c.BankTransactionsUnmatched = pos.UnmatchedCount
		return err
	}}
}

// PayoutCounter — выплаты в failed.
func PayoutCounter(store payoutdomain.Store) CounterFunc {
	return CounterFunc{Label: "payout", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		totals, err := store.AdminTotals(ctx)
		for _, t := range totals {
			if t.Status == payoutdomain.StatusFailed {
				c.PayoutsFailed = t.Count
			}
		}
		return err
	}}
}

// MarkingCounter — объекты ОРД в error.
func MarkingCounter(o markingdomain.Overview) CounterFunc {
	return CounterFunc{Label: "marking", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		counts, err := o.CountByStatus(ctx)
		for _, byStatus := range counts {
			c.OrdErrors += byStatus[markingdomain.SyncError]
		}
		return err
	}}
}

// OrdReportCounter — отчёт ОРД за прошлый месяц не сдан (R-16, R-23).
func OrdReportCounter(svc *markingapp.Service) CounterFunc {
	return CounterFunc{Label: "ord_report", Fn: func(ctx context.Context, c *adminapp.Counters) error {
		pending, err := svc.PendingReport(ctx)
		c.OrdReportPending = pending
		return err
	}}
}
