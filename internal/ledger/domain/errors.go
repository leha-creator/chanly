package domain

import (
	"errors"
	"fmt"

	"github.com/leha-creator/chanly/internal/platform/money"
)

var (
	// ErrNotFound — записи нет.
	ErrNotFound = errors.New("ledger: not found")
	// ErrInvalidPosting — проводка нарушает правило вида (R-4): ошибка программы.
	ErrInvalidPosting = errors.New("ledger: invalid posting")
	// ErrIdempotencyConflict — ключ уже использован транзакцией с другим составом (R-2).
	ErrIdempotencyConflict = errors.New("ledger: idempotency key reused with different postings")
	// ErrNoTransaction — журнал вызван вне транзакции вызывающего (R-5, I-10).
	ErrNoTransaction = errors.New("ledger: database transaction is required")
	// ErrInvariant — после снятия или распределения остаток удержания не ноль (I-12).
	ErrInvariant = errors.New("ledger: invariant violated")
)

// NegativeLiabilityError — проводка увела бы пассив в минус (06 §2.2 п. 4).
type NegativeLiabilityError struct {
	Account AccountRef
	Missing money.Kopecks
}

func (e *NegativeLiabilityError) Error() string {
	return fmt.Sprintf("ledger: account %s would become negative: missing %d", e.Account.Kind, e.Missing)
}
