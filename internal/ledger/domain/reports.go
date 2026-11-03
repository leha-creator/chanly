package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Entry — проводка по счёту для операций кабинета (R-9): кредит увеличивает пассив.
type Entry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	Kind          Kind
	Amount        money.Kopecks
	Credit        bool
	PlacementID   uuid.UUID
	InvoiceID     uuid.UUID
	CreatedAt     time.Time
}

// EntryCursor — позиция в списке проводок (время транзакции, id проводки).
type EntryCursor struct {
	At time.Time
	ID uuid.UUID
}

// SettlementLine — распределение удержания размещения: выплата блогеру и комиссия (R-16).
type SettlementLine struct {
	PlacementID  uuid.UUID
	AdvertiserID uuid.UUID
	Payout       money.Kopecks
	Fee          money.Kopecks
	SettledAt    time.Time
}

// CacheMismatch — кеш остатка счёта не равен сумме проводок.
type CacheMismatch struct {
	AccountID uuid.UUID
	Kind      AccountKind
	Cached    money.Kopecks
	Computed  money.Kopecks
}

// HoldMismatch — остаток placement_hold не равен сумме активного удержания (или не ноль без него).
type HoldMismatch struct {
	PlacementID uuid.UUID
	Balance     money.Kopecks
	Held        money.Kopecks
}

// NegativeAccount — пассив с отрицательным остатком.
type NegativeAccount struct {
	AccountID uuid.UUID
	Kind      AccountKind
	Balance   money.Kopecks
}

// Reports — чтение журнала. Реализация — ledger/postgres.
type Reports interface {
	// Entries — проводки счёта, новые сверху.
	Entries(ctx context.Context, ref AccountRef, after *EntryCursor, limit int) ([]Entry, error)
	// Settlements — распределения удержаний в [from, to).
	Settlements(ctx context.Context, from, to time.Time) ([]SettlementLine, error)
	// SidesTotal — сумма остатков активов и пассивов по кешу.
	SidesTotal(ctx context.Context) (assets, liabilities money.Kopecks, err error)
	CacheMismatches(ctx context.Context) ([]CacheMismatch, error)
	HoldMismatches(ctx context.Context) ([]HoldMismatch, error)
	NegativeLiabilities(ctx context.Context) ([]NegativeAccount, error)
	// Journal — проводки по отбору, новые сверху.
	Journal(ctx context.Context, f JournalFilter) ([]JournalLine, error)
}

// JournalFilter — отбор журнала денег: организация, вид транзакции, с момента.
type JournalFilter struct {
	OrganizationID uuid.UUID
	Kind           Kind
	Since          time.Time
	Limit          int
}

// JournalLine — проводка журнала с видами счетов (служебная команда, 06 §2.4).
type JournalLine struct {
	TransactionID uuid.UUID
	Kind          Kind
	CreatedAt     time.Time
	Reason        string
	Debit         AccountRef
	Credit        AccountRef
	Amount        money.Kopecks
}

// AdminEntryFilter — отбор журнала денег администратора (adm/ledger); нулевые поля — без отбора.
type AdminEntryFilter struct {
	OrganizationID *uuid.UUID
	Kind           *Kind
	From, To       *time.Time
	Offset, Limit  int
}

// AdminEntry — проводка журнала администратора: основание, действующее лицо и второй администратор.
type AdminEntry struct {
	JournalLine
	ActorType   ActorType
	ActorUserID *uuid.UUID
	// ApproverID — второй администратор ручной операции; uuid.Nil — не было.
	ApproverID  uuid.UUID
	PlacementID *uuid.UUID
	InvoiceID   *uuid.UUID
	PayoutID    *uuid.UUID
}

// AdminJournal — журнал денег администратора с общим числом строк.
type AdminJournal interface {
	AdminEntries(ctx context.Context, f AdminEntryFilter) ([]AdminEntry, int, error)
}
