package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Incoming — поступление на расчётный счёт платформы из выписки (05 §5).
type Incoming struct {
	// ExternalID — идентификатор операции; повтор не зачисляется (R-12).
	ExternalID    string
	OperationDate time.Time
	Amount        money.Kopecks
	PayerINN      string
	PayerName     string
	PayerAccount  string
	Purpose       string
}

// Statement — разобранная выписка: счёт, период, остатки и поступления.
type Statement struct {
	Account  string
	DateFrom *time.Time
	DateTo   *time.Time
	Opening  *money.Kopecks
	Closing  *money.Kopecks
	Incoming []Incoming
}

// StatementParser — «список поступлений» из файла выписки (05 §5, A-35). Реализация —
// integration/bank/clientbank; при переходе на API банка меняется только адаптер (Q-26).
type StatementParser interface {
	Parse(data []byte) (Statement, error)
}

// MatchStatus — результат сопоставления поступления (bank_transaction.match_status).
type MatchStatus string

const (
	MatchMatched   MatchStatus = "matched"
	MatchUnmatched MatchStatus = "unmatched"
	MatchIgnored   MatchStatus = "ignored"
)

// BankTransaction — сохранённое поступление.
type BankTransaction struct {
	ID          uuid.UUID
	StatementID uuid.UUID
	Incoming
	MatchStatus    MatchStatus
	InvoiceID      uuid.UUID
	OrganizationID uuid.UUID
	MatchedBy      uuid.UUID
	IgnoredReason  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BankStatement — загруженный файл выписки и итог разбора (R-12).
type BankStatement struct {
	ID             uuid.UUID
	UploadedBy     uuid.UUID
	StorageKey     string
	FileSHA256     string
	Account        string
	DateFrom       *time.Time
	DateTo         *time.Time
	Opening        *money.Kopecks
	Closing        *money.Kopecks
	IncomingCount  int
	MatchedCount   int
	UnmatchedCount int
	DuplicateCount int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
