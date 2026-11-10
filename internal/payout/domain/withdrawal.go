package domain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type WithdrawalDestination struct {
	CompanyINN, BIC, AccountNumber string
	ExecutorID                     int64
}
type WithdrawalTerms struct {
	Destination           WithdrawalDestination
	Amount, MaxCommission money.Kopecks
	AdminID, ApproverID   uuid.UUID
	Reason                string
}

func (t WithdrawalTerms) Snapshot() ([]byte, string, error) {
	digits := func(s string, n int) bool {
		if len(s) != n {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	d := t.Destination
	if t.Amount <= 0 || t.MaxCommission < 0 || int64(t.Amount) > math.MaxInt64-int64(t.MaxCommission) || t.AdminID == uuid.Nil || t.ApproverID == uuid.Nil || t.AdminID == t.ApproverID || strings.TrimSpace(t.Reason) == "" || d.ExecutorID <= 0 || (!digits(d.CompanyINN, 10) && !digits(d.CompanyINN, 12)) || !digits(d.BIC, 9) || !digits(d.AccountNumber, 20) {
		return nil, "", ErrInvalidPayout
	}
	b, err := json.Marshal(t)
	return b, fmt.Sprintf("%x", sha256.Sum256(b)), err
}

type Withdrawal struct {
	ID          uuid.UUID
	Scope       ProviderScope
	Terms       WithdrawalTerms
	Hash, State string
	TaskID      *int64
}
type WithdrawalProvider interface {
	CreateWithdrawal(context.Context, string, WithdrawalTerms) (ProviderTask, error)
}
type WithdrawalStore interface {
	PrepareWithdrawal(context.Context, Withdrawal) (Withdrawal, error)
	Withdrawal(context.Context, uuid.UUID, bool) (Withdrawal, error)
	ClaimWithdrawal(context.Context, uuid.UUID) (bool, error)
	AttachWithdrawal(context.Context, uuid.UUID, int64) error
	SettleWithdrawal(context.Context, uuid.UUID) error
}
type WithdrawalLedger interface {
	Withdrawal(context.Context, Withdrawal, money.Kopecks) error
}
