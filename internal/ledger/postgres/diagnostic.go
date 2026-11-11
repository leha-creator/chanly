package postgres

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// DiagnosticExpense uses the narrow SQL capability rather than granting the
// diagnostic runner INSERT/UPDATE on business ledger tables.
func (s *Store) DiagnosticExpense(ctx context.Context, runID uuid.UUID, nominal, fee money.Kopecks) (uuid.UUID, error) {
	if !s.InTransaction(ctx) || nominal <= 0 || fee < 0 || int64(nominal) > math.MaxInt64-int64(fee) {
		return uuid.Nil, domain.ErrInvalidPosting
	}
	t := domain.NewDiagnosticExpense(runID, nominal, fee)
	if err := t.Validate(); err != nil {
		return uuid.Nil, err
	}
	return s.q(ctx).RecordDiagnosticExpense(ctx, sqlc.RecordDiagnosticExpenseParams{RunID: runID, Nominal: int64(nominal), Fee: int64(fee), EntriesHash: t.EntriesHash(), Ids: []uuid.UUID{ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()}})
}
