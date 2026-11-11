package rocketwork

import (
	"encoding/json"
	"fmt"
)

// Wire DTOs deliberately do not share domain state enums. Provider additions
// are tolerated on reads, but never imply payment readiness or finality.
type pageDTO struct {
	Current int `json:"current"`
	Last    int `json:"last"`
	Total   int `json:"total"`
}

func (p *pageDTO) validate(page, count int) error {
	if p == nil || p.Current != page || p.Last < page || p.Total < count || p.Total < 0 {
		return fmt.Errorf("%w: invalid pagination metadata", ErrFormat)
	}
	return nil
}

type accountDTO struct {
	Balance    json.Number `json:"balance"`
	CompanyINN string      `json:"company_inn"`
}

type taskDTO struct {
	PaidAt *string `json:"paid_at"`
	Taxes  *struct {
		Requested *json.Number `json:"requested_amount"`
		Retained  *json.Number `json:"retained_amount"`
	} `json:"taxes"`
	SignatureForm        string          `json:"signature_form"`
	SignedAt             json.RawMessage `json:"signed_at"`
	ExecutorSignedAt     json.RawMessage `json:"executor_signed_at"`
	ID                   int64           `json:"id"`
	ExternalID           string          `json:"external_id"`
	Type                 string          `json:"type"`
	Status               string          `json:"status"`
	AmountGross          *string         `json:"amount_gross"`
	ExecutorAmountGross  *string         `json:"executor_amount_gross"`
	Commission           *string         `json:"commission"`
	EmploymentKind       string          `json:"current_employment_kind"`
	Executor             *executorDTO    `json:"executor"`
	TaxRetention         *bool           `json:"tax_retention"`
	ActRequired          *bool           `json:"act_required"`
	ReceiptURI           string          `json:"receipt_uri"`
	AgreementURI         string          `json:"agreement_uri"`
	AgreementSigningURI  string          `json:"agreement_signing_uri"`
	AcceptanceURI        string          `json:"acceptance_uri"`
	AcceptanceSigningURI string          `json:"acceptance_signing_uri"`
	ReceiptsLinks        json.RawMessage `json:"receipts_links"`
	ManualFNSReceipt     json.RawMessage `json:"manual_fns_receipt"`
}

type executorDTO struct {
	ID                 int64                    `json:"id"`
	ExternalID         string                   `json:"external_id"`
	Type               string                   `json:"type"`
	INN                string                   `json:"inn"`
	Phone              string                   `json:"phone"`
	Email              string                   `json:"email"`
	EmploymentKind     string                   `json:"employment_kind"`
	PaymentReadiness   string                   `json:"payment_readiness"`
	SelfemployedStatus string                   `json:"selfemployed_status"`
	Profiles           []selfemployedProfileDTO `json:"selfemployed_profiles"`
}

type selfemployedProfileDTO struct {
	Provider  string `json:"provider"`
	Status    string `json:"selfemployed_status"`
	Agreement *struct {
		Status *string `json:"status"`
	} `json:"agreement"`
}

func requiredMoney(value *string) (int64, error) {
	if value == nil {
		return 0, fmt.Errorf("%w: missing monetary value", ErrFormat)
	}
	amount, err := rublesToKopecks(*value)
	if err != nil || amount < 0 {
		return 0, fmt.Errorf("%w: invalid monetary value", ErrFormat)
	}
	return amount, nil
}
