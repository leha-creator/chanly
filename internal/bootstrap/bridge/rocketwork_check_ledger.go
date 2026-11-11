package bridge

import (
	"context"

	"github.com/google/uuid"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type CheckLedger struct{ Ledger *ledgerapp.Service }

func (b CheckLedger) DiagnosticExpense(ctx context.Context, run string, nominal, fee money.Kopecks) (string, error) {
	id, err := uuid.Parse(run)
	if err != nil {
		return "", err
	}
	result, err := b.Ledger.DiagnosticExpense(ctx, id, nominal, fee)
	return result.String(), err
}
