package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Операции администратора через HTTP (этап «API администратора», R-2): сумма выше порога ждёт
// второго подтверждения — исполнение по описанию (ManualPayload, TopupPayload) делает
// переходник модуля admin после решения другого senior.

// NeedsApproval — ручная операция на сумму amount требует второго администратора (R-14).
func (s *Service) NeedsApproval(amount money.Kopecks) bool {
	return s.ManualSecondApproval > 0 && amount > s.ManualSecondApproval
}

// ManualPayload — описание ручной проводки в запросе подтверждения.
type ManualPayload struct {
	Debit  domain.AccountRef `json:"debit"`
	Credit domain.AccountRef `json:"credit"`
	Amount money.Kopecks     `json:"amount"`
	Reason string            `json:"reason"`
}

// TopupPayload — описание пополнения депозита в запросе подтверждения.
type TopupPayload struct {
	Amount money.Kopecks `json:"amount"`
	Reason string        `json:"reason"`
}

// CheckAccounts проверяет счета ручной проводки до запроса подтверждения: неверный счёт —
// ошибка сразу, а не после решения второго администратора.
func CheckAccounts(debit, credit domain.AccountRef) error {
	if err := debit.Validate(); err != nil {
		return err
	}
	if err := credit.Validate(); err != nil {
		return err
	}
	if debit == credit {
		return errors.New("ledger: debit and credit are the same account")
	}
	return nil
}

// AdminEntries — журнал денег администратора.
func (s *Service) AdminEntries(ctx context.Context, f domain.AdminEntryFilter) ([]domain.AdminEntry, int, error) {
	if s.Admin == nil {
		return nil, 0, errors.New("ledger: admin journal is not wired")
	}
	return s.Admin.AdminEntries(ctx, f)
}

// Summary — сверка журнала (06 §2.4): остатки по видам и равенство активов и пассивов.
type Summary struct {
	Totals                map[domain.AccountKind]money.Kopecks
	Assets, Liabilities   money.Kopecks
	AdvertiserLiabilities money.Kopecks
}

// AdminSummary — сверка журнала без внешних вызовов.
func (s *Service) AdminSummary(ctx context.Context) (Summary, error) {
	totals, err := s.Store.BalancesByKind(ctx)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{Totals: totals,
		AdvertiserLiabilities: totals[domain.AccountAdvertiserBalance] + totals[domain.AccountPlacementHold]}
	if out.Assets, out.Liabilities, err = s.Reports.SidesTotal(ctx); err != nil {
		return Summary{}, err
	}
	return out, nil
}

// ManualFromPayload — ручная проводка по подтверждённому запросу.
func ManualFromPayload(key string, p ManualPayload, adminID, approverID uuid.UUID) ManualRequest {
	return ManualRequest{Key: key, Debit: p.Debit, Credit: p.Credit, Amount: p.Amount, AdminID: adminID,
		ApproverID: approverID, Reason: p.Reason}
}
