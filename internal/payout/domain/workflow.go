package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// ProviderScope separates test doubles and accounts even when provider IDs
// happen to coincide. Account is a configured stable account key, not a token.
type ProviderScope struct{ Environment, Account string }

func (s ProviderScope) Validate() error {
	switch s.Environment {
	case "fake", "local-emulator", "sandbox", "production":
	default:
		return fmt.Errorf("%w: provider environment", ErrInvalidPayout)
	}
	if strings.TrimSpace(s.Account) == "" || len(s.Account) > 200 {
		return fmt.Errorf("%w: provider account", ErrInvalidPayout)
	}
	return nil
}

type ExecutorBinding struct {
	Scope                                   ProviderScope
	OrganizationID                          uuid.UUID
	ExecutorID                              int64
	LookupKind, LookupValue, EmploymentKind string
	VerifiedAt                              time.Time
}

func (b ExecutorBinding) Validate() error {
	if err := b.Scope.Validate(); err != nil {
		return err
	}
	if b.OrganizationID == uuid.Nil || b.ExecutorID <= 0 || b.VerifiedAt.IsZero() || b.LookupValue == "" || len(b.LookupValue) > 320 {
		return fmt.Errorf("%w: executor binding", ErrInvalidPayout)
	}
	switch b.LookupKind {
	case "inn", "phone", "email":
	default:
		return fmt.Errorf("%w: executor lookup", ErrInvalidPayout)
	}
	if b.EmploymentKind != "entrepreneur" && b.EmploymentKind != "selfemployed" {
		return fmt.Errorf("%w: employment kind", ErrInvalidPayout)
	}
	return nil
}

// AgreementTerms is the immutable, approved business snapshot. No provider
// response or passport/bank payload belongs here. Booleans are explicit.
type AgreementTerms struct {
	Amount              money.Kopecks `json:"amount_kopecks"`
	Currency            string        `json:"currency"`
	ExecutorID          int64         `json:"executor_id"`
	EmploymentKind      string        `json:"employment_kind"`
	Title               string        `json:"title"`
	Description         string        `json:"description"`
	SignatureForm       string        `json:"signature_form"`
	DocumentName        string        `json:"document_name,omitempty"`
	DocumentNumber      string        `json:"document_number,omitempty"`
	DocumentDate        string        `json:"document_date,omitempty"`
	PeriodFrom          string        `json:"period_from,omitempty"`
	PeriodTo            string        `json:"period_to,omitempty"`
	ActRequired         bool          `json:"act_required"`
	TaxRetention        bool          `json:"tax_retention"`
	Autosignable        bool          `json:"autosignable"`
	Autocomplete        bool          `json:"autocomplete"`
	Autopayable         bool          `json:"autopayable"`
	MaxCommission       money.Kopecks `json:"max_commission_kopecks"`
	CommissionUnlimited bool          `json:"commission_unlimited,omitempty"`
}

func (a AgreementTerms) Validate() error {
	if a.Amount <= 0 || a.Currency != "RUB" || a.ExecutorID <= 0 || a.Title == "" || a.Description == "" || a.MaxCommission < 0 {
		return fmt.Errorf("%w: agreement terms", ErrInvalidPayout)
	}
	if a.CommissionUnlimited && a.MaxCommission != 0 {
		return fmt.Errorf("%w: conflicting commission policy", ErrInvalidPayout)
	}
	if a.EmploymentKind != "entrepreneur" && a.EmploymentKind != "selfemployed" {
		return fmt.Errorf("%w: agreement employment", ErrInvalidPayout)
	}
	date := func(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }
	switch a.SignatureForm {
	case "online":
		if a.DocumentName == "" || !date(a.PeriodFrom) || !date(a.PeriodTo) || a.PeriodTo < a.PeriodFrom || a.DocumentNumber != "" || a.DocumentDate != "" {
			return fmt.Errorf("%w: online agreement", ErrInvalidPayout)
		}
	case "manual":
		if a.DocumentNumber == "" || !date(a.DocumentDate) || a.DocumentName != "" {
			return fmt.Errorf("%w: manual agreement", ErrInvalidPayout)
		}
	default:
		return fmt.Errorf("%w: signature form", ErrInvalidPayout)
	}
	return nil
}

func (a AgreementTerms) AllowsCommission(fee money.Kopecks) bool {
	return fee >= 0 && (a.CommissionUnlimited || fee <= a.MaxCommission)
}

func (a AgreementTerms) Snapshot() ([]byte, string, error) {
	if err := a.Validate(); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(a)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(body)
	return body, hex.EncodeToString(hash[:]), nil
}

type OperationState string

const (
	OperationPrepared  OperationState = "prepared"
	OperationInFlight  OperationState = "in_flight"
	OperationConfirmed OperationState = "confirmed"
	OperationUncertain OperationState = "uncertain"
	OperationRejected  OperationState = "rejected"
)

// No clock/lease expiry can authorize resending an in-flight request. Recovery
// reads the provider and records evidence before confirming or rejecting it.
func CanTransitionOperation(from, to OperationState) bool {
	switch from {
	case OperationPrepared:
		return to == OperationInFlight || to == OperationRejected
	case OperationInFlight:
		return to == OperationConfirmed || to == OperationUncertain || to == OperationRejected
	case OperationUncertain:
		return to == OperationConfirmed || to == OperationRejected
	}
	return false
}

type Workflow struct {
	ObservedStatus       string
	Documents            PayoutDocuments
	PayoutID             uuid.UUID
	Scope                ProviderScope
	ExternalID           string
	TaskID               *int64
	Terms                AgreementTerms
	TermsHash            string
	Phase                string
	CreatedAt, UpdatedAt time.Time
}

type WorkflowOperation struct {
	PayoutID      uuid.UUID
	Action        string
	State         OperationState
	PayloadHash   string
	CorrelationID uuid.UUID
	Evidence      string
	UpdatedAt     time.Time
}

// WorkflowStore shares the caller's transaction with payout/job writes. Claim
// is a compare-and-set transition; no external network call occurs in a tx.
type WorkflowStore interface {
	BindExecutor(context.Context, ExecutorBinding) (ExecutorBinding, error)
	ExecutorBinding(context.Context, ProviderScope, uuid.UUID) (ExecutorBinding, error)
	PrepareWorkflow(context.Context, Workflow) (Workflow, error)
	Workflow(context.Context, uuid.UUID, bool) (Workflow, error)
	UpdateWorkflow(context.Context, Workflow) error
	PrepareOperation(context.Context, WorkflowOperation) (WorkflowOperation, error)
	Operation(context.Context, uuid.UUID, string) (WorkflowOperation, error)
	TransitionOperation(context.Context, WorkflowOperation, OperationState) (bool, error)
}

type ExecutorLookup struct {
	OrganizationID                                      uuid.UUID
	LookupKind, LookupValue, INN, Phone, EmploymentKind string
	ExpectedID                                          int64
}

type ExecutorResolution struct {
	ExecutorID         int64
	Status             ContractorStatus
	Reason, NextAction string
}

type ExecutorReader interface {
	ResolveExecutor(context.Context, ExecutorLookup) (ExecutorResolution, error)
}

type ProviderTask struct {
	RetainedTax                              *money.Kopecks
	Documents                                PayoutDocuments
	ID                                       int64
	ExternalID, Type, Status, EmploymentKind string
	ExecutorID                               int64
	ExecutorINN                              string
	Amount                                   money.Kopecks
	ExecutorAmount, Commission               *money.Kopecks
	TaxRetention, ActRequired                *bool
	ReceiptURL, SignatureForm                string
	CustomerSigned, ExecutorSigned           *bool
}

type ReceiptDocument struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	RefundURL string `json:"refund_url,omitempty"`
}
type PayoutDocuments struct {
	AgreementURL         string            `json:"agreement_url,omitempty"`
	AgreementSigningURL  string            `json:"agreement_signing_url,omitempty"`
	AcceptanceURL        string            `json:"acceptance_url,omitempty"`
	AcceptanceSigningURL string            `json:"acceptance_signing_url,omitempty"`
	ReceiptURL           string            `json:"receipt_url,omitempty"`
	FNS                  []ReceiptDocument `json:"fns,omitempty"`
	Fiscal               []ReceiptDocument `json:"fiscal,omitempty"`
	ManualRequired       *bool             `json:"manual_required,omitempty"`
	ManualStatus         string            `json:"manual_status,omitempty"`
	ManualFormURL        string            `json:"manual_form_url,omitempty"`
}

type TaskOrder struct {
	ExternalID, ExecutorINN string
	Terms                   AgreementTerms
}
type TaskProvider interface {
	CreateTask(context.Context, TaskOrder) (ProviderTask, error)
	ReadTask(context.Context, int64) (ProviderTask, error)
	FindTask(context.Context, string) (ProviderTask, bool, error)
}

type TaskActions interface {
	ActOnTask(context.Context, int64, string) (ProviderTask, error)
}
