package domain

import (
	"context"
	"github.com/leha-creator/chanly/internal/platform/money"
	"time"
)

// ControlStore is backed only by the durable trusted control database.
type ControlStore interface {
	Run(context.Context, string) (Run, error)
	Grant(context.Context, string) (Grant, error)
	Prepare(context.Context, Run) (Run, error)
	Checkpoint(context.Context, string, Action) (Checkpoint, error)
	Now(context.Context) (time.Time, error)
	Intent(context.Context, string, Action, string) (bool, error)
	Observe(context.Context, string, Action, string, *int64) error
	Settle(context.Context, string, money.Kopecks, money.Kopecks, string) error
	AccountForAlias(context.Context, string) (string, error)
	Freeze(context.Context, string) error
	Release(context.Context, string) error
	RecordPaidAt(context.Context, string, time.Time) error
}

// Ledger records diagnostic expenses without a business placement or payable.
// Both this call and Settle must participate in the same trusted transaction.
type Ledger interface {
	DiagnosticExpense(context.Context, string, money.Kopecks, money.Kopecks) (string, error)
}

type Provider interface {
	ValidateManifest(Manifest) error
	Inspect(context.Context, Run) (Observation, error)
	Write(context.Context, Run, Action, func(string) error) error
}

type EventRegistry interface {
	EventOwner(context.Context, string, string, int64) (string, error)
}
