package app

import (
	"context"
	"math"
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

// Settle is called only with authoritative final provider evidence. Unknown fees
// stay reserved. The ledger entry and release of the hold commit atomically.
func (s *Service) Settle(ctx context.Context, id string, nominal, fee money.Kopecks) error {
	if nominal < 0 || fee < 0 || int64(nominal) > math.MaxInt64-int64(fee) || s.Ledger == nil || s.Tx == nil {
		return domain.ErrDenied
	}
	run, err := s.Store.Run(ctx, id)
	if err != nil {
		return domain.ErrDenied
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		ledger, err := s.Ledger.DiagnosticExpense(ctx, id, nominal, fee)
		if err != nil {
			return err
		}
		return s.Store.Settle(ctx, id, nominal, fee, ledger)
	})
	if err != nil {
		// A confirmed provider charge must not be followed by more writes while
		// its ledger/settlement cannot commit. The hold itself remains untouched.
		freezeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		freezeErr := s.Store.Freeze(freezeCtx, id)
		cancel()
		if freezeErr != nil {
			s.Log.ErrorContext(ctx, "diagnostic freeze pending control DB recovery", "run", id)
		}
		s.Log.ErrorContext(ctx, "diagnostic settlement failed", "run", id)
		return err
	}
	s.Log.InfoContext(ctx, "diagnostic expense settled", "run", id, "nominal", nominal, "fee", fee)
	if nominal != run.Nominal || fee > run.FeeCeiling {
		s.Log.ErrorContext(ctx, "diagnostic financial discrepancy requires manual reconciliation", "run", id, "nominal", nominal, "fee", fee, "account_frozen", true)
		return domain.ErrFinancialMismatch
	}
	return nil
}
