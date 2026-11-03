package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Balance — остатки рекламодателя (R-9): свободно — advertiser_balance, удержано — сумма
// активных удержаний.
func (s *Service) Balance(ctx context.Context, orgID uuid.UUID) (available, held money.Kopecks, err error) {
	if available, err = s.Store.AccountBalance(ctx, domain.AdvertiserBalance(orgID)); err != nil {
		return 0, 0, err
	}
	if held, err = s.Store.HeldTotal(ctx, orgID); err != nil {
		return 0, 0, err
	}
	s.log.DebugContext(ctx, "balance read", logx.KeyOp, "Ledger.Balance", logx.KeyOrganizationID, orgID,
		"available", int64(available), "held", int64(held))
	return available, held, nil
}

// Totals — сумма остатков по видам счетов (сверка, метрики).
func (s *Service) Totals(ctx context.Context) (map[domain.AccountKind]money.Kopecks, error) {
	return s.Store.BalancesByKind(ctx)
}
