package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/money"
)

const (
	BankReadOnly   = "readonly"
	BankControlled = "controlled"
	BankAutomation = "automation"
)

var ErrBankCreditRestricted = errors.New("bank credit restricted")

func DenyBankCredit(reason string) error {
	return fmt.Errorf("%w: %s", ErrBankCreditRestricted, reason)
}

// BankCreditPolicy binds one finite dev run to its account and organization.
// Changing limits or extending expiry requires a new explicit run identifier.
type BankCreditPolicy struct {
	RunID, OrganizationID uuid.UUID
	Account, Origin       string
	MaxPayment, MaxTotal  money.Kopecks
	ExpiresAt             time.Time
}

func (p BankCreditPolicy) Marker() string { return "DEV-TBANK-" + p.RunID.String() }

func (p BankCreditPolicy) Check(org uuid.UUID, amount money.Kopecks, now time.Time) error {
	if p.RunID == uuid.Nil || p.OrganizationID == uuid.Nil || p.Account == "" || p.Origin == "" || p.MaxPayment <= 0 || p.MaxTotal < p.MaxPayment || p.ExpiresAt.IsZero() {
		return DenyBankCredit("incomplete_policy")
	}
	if !now.Before(p.ExpiresAt) {
		return DenyBankCredit("expired")
	}
	if org != p.OrganizationID {
		return DenyBankCredit("organization_not_allowed")
	}
	if amount <= 0 || amount > p.MaxPayment {
		return DenyBankCredit("payment_limit")
	}
	return nil
}

// BankCreditBudget reserves funds in the same transaction as deposit and notice.
type BankCreditBudget interface {
	ReserveBankCredit(context.Context, BankCreditPolicy, uuid.UUID, money.Kopecks, time.Time) error
}
