package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type diagnosticStore interface {
	DiagnosticExpense(context.Context, uuid.UUID, money.Kopecks, money.Kopecks) (uuid.UUID, error)
}

func (s *Service) DiagnosticExpense(ctx context.Context, runID uuid.UUID, nominal, fee money.Kopecks) (uuid.UUID, error) {
	store, ok := s.Store.(diagnosticStore)
	if !ok {
		return uuid.Nil, domain.ErrInvalidPosting
	}
	id, err := store.DiagnosticExpense(ctx, runID, nominal, fee)
	if err != nil {
		s.log.ErrorContext(ctx, "diagnostic ledger posting failed", "run", runID)
		return uuid.Nil, err
	}
	s.log.InfoContext(ctx, "diagnostic ledger expense recorded", "run", runID, "ledger_id", id, "nominal", nominal, "fee", fee)
	return id, nil
}
