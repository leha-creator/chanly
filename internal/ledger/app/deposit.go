package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Deposit зачисляет сопоставленное поступление на баланс рекламодателя (S-30) в транзакции
// вызывающего: автоматически — от системы, вручную — от администратора adminID.
func (s *Service) Deposit(ctx context.Context, bankTxID, invoiceID, orgID uuid.UUID, amount money.Kopecks, adminID uuid.UUID) (Result, error) {
	actor := domain.ActorSystem
	if adminID != uuid.Nil {
		actor = domain.ActorAdmin
	}
	return s.Post(ctx, domain.NewDeposit(bankTxID, invoiceID, orgID, amount, actor, adminID))
}

// Settlements — распределения удержаний в [from, to) для закрывающих документов (R-16).
func (s *Service) Settlements(ctx context.Context, from, to time.Time) ([]domain.SettlementLine, error) {
	return s.Reports.Settlements(ctx, from, to)
}
