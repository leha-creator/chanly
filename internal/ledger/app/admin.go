package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Действия журнала действий для ручных операций (R-14, 07 §4).
const (
	ActionManual        = "ledger.manual"
	ActionBalanceReturn = "ledger.balance_return"
	ActionDepositTopup  = "ledger.deposit_topup"
)

// ErrSecondApproval — ручная проводка выше порога без второго администратора (R-14).
var ErrSecondApproval = errors.New("ledger: second senior admin approval is required")

// ManualRequest — ручная корректировка одной проводкой (S-19, R-14). Key — ключ команды:
// повтор с тем же ключом не создаёт второй проводки (R-3).
type ManualRequest struct {
	Key        string
	Debit      domain.AccountRef
	Credit     domain.AccountRef
	Amount     money.Kopecks
	AdminID    uuid.UUID
	ApproverID uuid.UUID
	Reason     string
}

// Manual проводит ручную корректировку: основание обязательно, сумма больше порога —
// только со вторым администратором (R-14); запись в журнале действий.
func (s *Service) Manual(ctx context.Context, r ManualRequest) (Result, error) {
	const op = "Ledger.Manual"
	if r.Reason == "" {
		return Result{}, apperr.Validation("reason", "required")
	}
	if s.ManualSecondApproval > 0 && r.Amount > s.ManualSecondApproval && (r.ApproverID == uuid.Nil || r.ApproverID == r.AdminID) {
		s.log.WarnContext(ctx, "manual ledger operation refused", logx.KeyOp, op, "admin_id", r.AdminID, KeyAmount, int64(r.Amount),
			"reason", "second_approval")
		return Result{}, ErrSecondApproval
	}
	t := domain.NewManual(r.Key, []domain.Posting{{Debit: r.Debit, Credit: r.Credit, Amount: r.Amount}}, r.AdminID, r.Reason)
	data := map[string]any{"debit": r.Debit.String(), "credit": r.Credit.String(), KeyAmount: int64(r.Amount)}
	if r.ApproverID != uuid.Nil {
		data["approver_id"] = r.ApproverID.String()
	}
	return s.adminPost(ctx, op, t, ActionManual, nil, r.ApproverID, data)
}

// BalanceReturn — возврат свободного остатка рекламодателю переводом (D-26, R-14): не больше
// остатка advertiser_balance (иначе NegativeLiabilityError).
func (s *Service) BalanceReturn(ctx context.Context, key string, orgID uuid.UUID, amount money.Kopecks, adminID uuid.UUID, reason string) (Result, error) {
	const op = "Ledger.BalanceReturn"
	if reason == "" {
		return Result{}, apperr.Validation("reason", "required")
	}
	t := domain.NewBalanceReturn(key, orgID, amount, adminID, reason)
	return s.adminPost(ctx, op, t, ActionBalanceReturn, &orgID, uuid.Nil, map[string]any{KeyAmount: int64(amount)})
}

// DepositTopup — пополнение депозита Рокет Ворк с расчётного счёта (06 §2.3, R-12 этапа выплат):
// bank → rocketwork_deposit. Сумма выше порога — только со вторым администратором (R-14).
func (s *Service) DepositTopup(ctx context.Context, key string, amount money.Kopecks, adminID, approverID uuid.UUID, reason string) (Result, error) {
	const op = "Ledger.DepositTopup"
	if reason == "" {
		return Result{}, apperr.Validation("reason", "required")
	}
	if s.ManualSecondApproval > 0 && amount > s.ManualSecondApproval && (approverID == uuid.Nil || approverID == adminID) {
		s.log.WarnContext(ctx, "admin deposit topup refused", logx.KeyOp, op, "admin_id", adminID, KeyAmount, int64(amount),
			"reason", "second_approval")
		return Result{}, ErrSecondApproval
	}
	t := domain.NewDepositTopup(key, amount, adminID, reason)
	data := map[string]any{KeyAmount: int64(amount), "key": key}
	if approverID != uuid.Nil {
		data["approver_id"] = approverID.String()
	}
	res, err := s.adminPost(ctx, op, t, ActionDepositTopup, nil, approverID, data)
	if err == nil {
		s.log.WarnContext(ctx, "admin deposit topup", logx.KeyOp, op, "admin_user_id", adminID, KeyAmount, int64(amount),
			"key", key, "replayed", res.Replayed)
	}
	return res, err
}

// adminPost проводит транзакцию администратора и пишет журнал действий; повтор ключа —
// без новой записи.
func (s *Service) adminPost(ctx context.Context, op string, t domain.Transaction, action string, orgID *uuid.UUID,
	approverID uuid.UUID, data map[string]any) (Result, error) {
	var res Result
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if res, err = s.Post(ctx, t); err != nil || res.Replayed {
			return err
		}
		if s.Audit == nil {
			return nil
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: action, ObjectType: "ledger_transaction", ObjectID: res.ID.String(), Reason: &t.Reason,
			ActorType: audit.ActorAdmin, OrganizationID: orgID, Data: data,
		})
	})
	if err != nil {
		return Result{}, err
	}
	s.log.WarnContext(ctx, "manual ledger operation", logx.KeyOp, op, KeyKind, t.Kind, KeyTransactionID, res.ID,
		"admin_id", t.ActorUserID, "approver_id", approverID, KeyAmount, int64(t.Total()), "replayed", res.Replayed)
	return res, nil
}

// Journal — журнал денег по отбору (служебная команда maxim admin money ledger list).
func (s *Service) Journal(ctx context.Context, f domain.JournalFilter) ([]domain.JournalLine, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	return s.Reports.Journal(ctx, f)
}
