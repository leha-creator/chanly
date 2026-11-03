package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// HoldStatus — состояние удержания (04 §7, hold.status).
type HoldStatus string

const (
	HoldActive      HoldStatus = "active"
	HoldReleased    HoldStatus = "released"
	HoldDistributed HoldStatus = "distributed"
)

// Hold — удержание по размещению и версии поста (R-6).
type Hold struct {
	ID            uuid.UUID
	PlacementID   uuid.UUID
	PostVersionID uuid.UUID
	// OrganizationID — рекламодатель.
	OrganizationID uuid.UUID
	Amount         money.Kopecks
	Status         HoldStatus
	CreatedAt      time.Time
	ClosedAt       *time.Time
}

// Account — счёт учёта с кешем остатка.
type Account struct {
	ID      uuid.UUID
	Ref     AccountRef
	Balance money.Kopecks
}

// Recorded — сохранённая транзакция (для повтора по ключу).
type Recorded struct {
	ID             uuid.UUID
	Kind           Kind
	IdempotencyKey string
	EntriesHash    string
	CreatedAt      time.Time
}

// Store — хранилище журнала. Реализация — ledger/postgres; все методы работают в транзакции
// из context (R-5). Единственный писатель ledger_* и hold (07 §9).
type Store interface {
	// InTransaction — в context открыта транзакция базы (R-5).
	InTransaction(ctx context.Context) bool
	// TransactionByKey — транзакция по ключу идемпотентности; нет — ErrNotFound.
	TransactionByKey(ctx context.Context, key string) (Recorded, error)
	// LockAccounts создаёт недостающие счета и блокирует все в порядке id (R-5).
	LockAccounts(ctx context.Context, refs []AccountRef, now time.Time) (map[AccountRef]Account, error)
	// InsertTransaction записывает транзакцию и проводки; счета уже заблокированы.
	InsertTransaction(ctx context.Context, id uuid.UUID, t Transaction, accounts map[AccountRef]Account, now time.Time) error
	// SetBalances обновляет кеш остатков.
	SetBalances(ctx context.Context, balances map[uuid.UUID]money.Kopecks, now time.Time) error

	// ActiveHoldForUpdate — активное удержание размещения под блокировкой; нет — ErrNotFound.
	ActiveHoldForUpdate(ctx context.Context, placementID uuid.UUID) (Hold, error)
	// ActiveHolds — активные удержания размещений.
	ActiveHolds(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]Hold, error)
	// InsertHold записывает активное удержание.
	InsertHold(ctx context.Context, h Hold) error
	// CloseHold закрывает удержание с состоянием status.
	CloseHold(ctx context.Context, id uuid.UUID, status HoldStatus, now time.Time) error

	// AccountBalance — кеш остатка без блокировки; счёта ещё нет — ноль.
	AccountBalance(ctx context.Context, ref AccountRef) (money.Kopecks, error)
	// HeldTotal — сумма активных удержаний рекламодателя.
	HeldTotal(ctx context.Context, orgID uuid.UUID) (money.Kopecks, error)
	// BalancesByKind — сумма остатков по видам счетов.
	BalancesByKind(ctx context.Context) (map[AccountKind]money.Kopecks, error)
}
