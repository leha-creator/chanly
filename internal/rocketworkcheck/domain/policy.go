// Package domain contains the trusted diagnostic policy, independent of transport.
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
)

type Access string

const (
	ReadOnly   Access = "readonly"
	Controlled Access = "controlled"
	Automation Access = "automation"
	Diagnostic        = "diagnostic"
	Business          = "business"
)

type Action string

const (
	Create   Action = "create"
	Complete Action = "complete"
	SignAct  Action = "sign_act"
	Pay      Action = "pay"
	Cancel   Action = "cancel"
)

var ErrDenied = errors.New("rocketwork diagnostic policy denied")
var ErrFinancialMismatch = errors.New("rocketwork diagnostic financial discrepancy")
var ErrWriteRejected = errors.New("rocketwork diagnostic write conclusively rejected")

// AccountID is the canonical physical provider account, never an environment alias.
// Only the policy owner can establish the alias-to-account mapping.
type Policy struct {
	// ArmingID is issued from trusted configuration outside control DB backups.
	ArmingID        string
	RunID           string
	ManifestHash    string
	AccountID       string
	CompanyINN      string
	Version         int64
	FixedAmount     money.Kopecks
	MaxCommission   money.Kopecks
	TotalBudget     money.Kopecks
	DailyBudget     money.Kopecks
	DailyOperations int64
	Executors       []Executor
	Actions         []Action
	NotBefore       time.Time
	ExpiresAt       time.Time
	Revoked         bool
	Killed          bool
}

type Executor struct {
	ID         int64
	INN        string
	Employment string
}

// UnmarshalJSON distinguishes an explicitly approved zero commission from a
// missing/null commission ceiling. Other monetary authority has no defaults either.
func (p *Policy) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ErrDenied
	}
	for _, key := range []string{"ArmingID", "RunID", "ManifestHash", "AccountID", "CompanyINN", "Version", "FixedAmount", "MaxCommission", "TotalBudget", "DailyBudget", "DailyOperations", "Executors", "Actions", "NotBefore", "ExpiresAt"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrDenied
		}
	}
	type policyJSON Policy
	var value policyJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return ErrDenied
	}
	*p = Policy(value)
	return nil
}

// Validate has no defaults: incomplete policies never become monetary authority.
func (p Policy) Validate() error {
	if strings.TrimSpace(p.AccountID) == "" || !digits(p.CompanyINN) || p.Version <= 0 ||
		p.FixedAmount <= 0 || p.MaxCommission < 0 || int64(p.FixedAmount) > math.MaxInt64-int64(p.MaxCommission) ||
		p.TotalBudget <= 0 || p.DailyBudget <= 0 || p.DailyBudget > p.TotalBudget ||
		p.FixedAmount+p.MaxCommission > p.DailyBudget || p.DailyOperations <= 0 ||
		p.NotBefore.IsZero() || !p.ExpiresAt.After(p.NotBefore) || len(p.Executors) == 0 || len(p.Actions) == 0 {
		return ErrDenied
	}
	for _, e := range p.Executors {
		if e.ID <= 0 || !digits(e.INN) || (e.Employment != "selfemployed" && e.Employment != "entrepreneur") {
			return ErrDenied
		}
	}
	for _, a := range p.Actions {
		if !slices.Contains([]Action{Create, Complete, SignAct, Pay, Cancel}, a) {
			return ErrDenied
		}
	}
	return nil
}

// Authorize uses trusted database time supplied by the caller, never client time.
func (p Policy) Authorize(now time.Time, account string, e Executor, amount money.Kopecks, action Action) error {
	if p.Validate() != nil || p.Revoked || p.Killed || now.Before(p.NotBefore) || !now.Before(p.ExpiresAt) ||
		account != p.AccountID || amount != p.FixedAmount || !slices.Contains(p.Actions, action) || !slices.Contains(p.Executors, e) {
		return ErrDenied
	}
	return nil
}

func digits(s string) bool {
	if len(s) != 10 && len(s) != 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
