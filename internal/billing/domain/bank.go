package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// BankQuery fixes the UTC interval across all pages of a read.
type BankQuery struct {
	Account  string
	From, To time.Time
	Cursor   string
}
type BankPage struct {
	Incoming                        []Incoming
	NextCursor                      string
	Opening, Closing, Credit, Debit *money.Kopecks
	Operations                      int
	Rejected                        int
}
type BankSource interface {
	ReadPage(context.Context, BankQuery) (BankPage, error)
}

// BankProgress is durable; a failed page is replayed through the same ingestion path.
type BankProgress struct {
	LedgerDifference                   *money.Kopecks
	Missing                            int
	BalanceMismatch                    bool
	Account                            string
	From, To, CompletedTo, LastSuccess time.Time
	Cursor                             string
	StatementID                        string
	Cursors                            []string
}
type BankSyncStore interface {
	MissingBankOperations(context.Context, string, time.Time, time.Time, uuid.UUID) (int, error)
	WithBankLock(context.Context, string, func(context.Context) error) error
	LoadBankProgress(context.Context, string) (BankProgress, error)
	SaveBankProgress(context.Context, BankProgress) error
}

// BankIdentity resolves source aliases inside the ingestion transaction.
type BankIdentity interface {
	ResolveIncoming(context.Context, string, Incoming) (Incoming, bool, error)
}
