package rocketwork

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type TaskTerms struct {
	ExternalID, ExecutorINN, EmploymentKind                                         string
	AmountKopecks                                                                   int64
	Title, Description                                                              string
	SignatureForm, DocumentName, DocumentNumber, DocumentDate, PeriodFrom, PeriodTo string
	ActRequired, TaxRetention, Autosignable, Autocomplete, Autopayable              bool
}

func (t TaskTerms) validate() error {
	if strings.TrimSpace(t.ExternalID) == "" || len(t.ExternalID) > 200 || t.AmountKopecks <= 0 || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Description) == "" {
		return fmt.Errorf("%w: task terms", ErrFormat)
	}
	if err := (ExecutorIdentity{Kind: "inn", Value: t.ExecutorINN, EmploymentKind: t.EmploymentKind}).validate(); err != nil {
		return err
	}
	date := func(v string) bool { _, err := time.Parse("2006-01-02", v); return err == nil }
	switch t.SignatureForm {
	case "online":
		if t.DocumentName == "" || !date(t.PeriodFrom) || !date(t.PeriodTo) || t.PeriodTo < t.PeriodFrom || t.DocumentNumber != "" || t.DocumentDate != "" {
			return fmt.Errorf("%w: online document terms", ErrFormat)
		}
	case "manual":
		if t.DocumentNumber == "" || !date(t.DocumentDate) || t.DocumentName != "" {
			return fmt.Errorf("%w: manual document terms", ErrFormat)
		}
	default:
		return fmt.Errorf("%w: explicit signature policy required", ErrFormat)
	}
	if t.Autocomplete && (t.SignatureForm != "manual" || t.ActRequired) {
		return fmt.Errorf("%w: unsupported autocomplete policy", ErrFormat)
	}
	return nil
}

func kopecksJSON(value int64) json.Number {
	return json.Number(fmt.Sprintf("%d.%02d", value/100, value%100))
}

type taskCreateDTO struct {
	Type           string           `json:"type"`
	ExternalID     string           `json:"external_id"`
	ExecutorINN    string           `json:"executor_inn"`
	EmploymentKind string           `json:"current_employment_kind"`
	AmountGross    json.Number      `json:"amount_gross"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	DocumentName   string           `json:"document_name,omitempty"`
	DocumentNumber string           `json:"document_number,omitempty"`
	DocumentDate   string           `json:"document_date,omitempty"`
	PeriodFrom     string           `json:"period_from,omitempty"`
	PeriodTo       string           `json:"period_to,omitempty"`
	ActRequired    bool             `json:"act_required"`
	TaxRetention   bool             `json:"tax_retention"`
	Autosignable   bool             `json:"autosignable"`
	Autocomplete   bool             `json:"autocomplete"`
	Autopayable    bool             `json:"autopayable"`
	Services       []taskServiceDTO `json:"services"`
}
type taskServiceDTO struct {
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
	UnitPrice   int64  `json:"unit_price_cents"`
	TotalPrice  int64  `json:"total_price_cents"`
}

func (t TaskTerms) wire() taskCreateDTO {
	return taskCreateDTO{Type: "OneTime", ExternalID: t.ExternalID, ExecutorINN: t.ExecutorINN, EmploymentKind: t.EmploymentKind, AmountGross: kopecksJSON(t.AmountKopecks), Title: t.Title, Description: t.Description, DocumentName: t.DocumentName, DocumentNumber: t.DocumentNumber, DocumentDate: t.DocumentDate, PeriodFrom: t.PeriodFrom, PeriodTo: t.PeriodTo, ActRequired: t.ActRequired, TaxRetention: t.TaxRetention, Autosignable: t.Autosignable, Autocomplete: t.Autocomplete, Autopayable: t.Autopayable, Services: []taskServiceDTO{{Description: t.Description, Quantity: 1, UnitPrice: t.AmountKopecks, TotalPrice: t.AmountKopecks}}}
}

// TaskSnapshot separates agreement/payment/document states from the legacy
// Payment port. Amounts are exact; optional financial data stays optional.
type TaskSnapshot struct {
	RetainedTaxKopecks                       *int64
	Documents                                TaskDocuments
	ID                                       int64
	ExternalID, Type, Status, EmploymentKind string
	ExecutorID                               int64
	ExecutorINN                              string
	AmountKopecks                            int64
	ExecutorAmountKopecks, CommissionKopecks *int64
	TaxRetention, ActRequired                *bool
	ReceiptURL                               string
	SignatureForm                            string
	CustomerSigned, ExecutorSigned           *bool
}

type ReceiptDocument struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	RefundURL string `json:"refund_url"`
}
type TaskDocuments struct {
	AgreementURL         string            `json:"agreement_url"`
	AgreementSigningURL  string            `json:"agreement_signing_url"`
	AcceptanceURL        string            `json:"acceptance_url"`
	AcceptanceSigningURL string            `json:"acceptance_signing_url"`
	FNS                  []ReceiptDocument `json:"fns"`
	Fiscal               []ReceiptDocument `json:"fiscal"`
	ManualRequired       *bool             `json:"manual_required"`
	ManualStatus         string            `json:"manual_status"`
	ManualFormURL        string            `json:"manual_form_url"`
}

func (d taskDTO) snapshot() (TaskSnapshot, error) {
	if d.ID <= 0 || (d.Type != "OneTime" && d.Type != "Withdrawal") || d.Status == "" {
		return TaskSnapshot{}, ErrFormat
	}
	amount, err := requiredMoney(d.AmountGross)
	if err != nil {
		return TaskSnapshot{}, err
	}
	s := TaskSnapshot{ID: d.ID, ExternalID: d.ExternalID, Type: d.Type, Status: d.Status, EmploymentKind: d.EmploymentKind, AmountKopecks: amount, TaxRetention: d.TaxRetention, ActRequired: d.ActRequired, ReceiptURL: d.ReceiptURI, SignatureForm: d.SignatureForm}
	if d.Taxes != nil && d.Taxes.Retained != nil {
		v, err := rublesToKopecks(string(*d.Taxes.Retained))
		if err != nil || v < 0 {
			return TaskSnapshot{}, ErrFormat
		}
		s.RetainedTaxKopecks = &v
	}
	if len(d.ReceiptsLinks) > 0 && string(d.ReceiptsLinks) != "null" {
		if err := json.Unmarshal(d.ReceiptsLinks, &s.Documents); err != nil {
			return TaskSnapshot{}, ErrFormat
		}
	}
	if len(d.ManualFNSReceipt) > 0 && string(d.ManualFNSReceipt) != "null" {
		var manual struct {
			Required *bool  `json:"required"`
			Status   string `json:"status"`
			URL      string `json:"form_url"`
		}
		if err := json.Unmarshal(d.ManualFNSReceipt, &manual); err != nil {
			return TaskSnapshot{}, ErrFormat
		}
		s.Documents.ManualRequired, s.Documents.ManualStatus, s.Documents.ManualFormURL = manual.Required, manual.Status, manual.URL
	}
	s.Documents.AgreementURL, s.Documents.AgreementSigningURL = d.AgreementURI, d.AgreementSigningURI
	s.Documents.AcceptanceURL, s.Documents.AcceptanceSigningURL = d.AcceptanceURI, d.AcceptanceSigningURI
	if d.Executor != nil {
		s.ExecutorID = d.Executor.ID
		s.ExecutorINN = d.Executor.INN
	}
	for _, pair := range []struct {
		in  *string
		out **int64
	}{{d.Commission, &s.CommissionKopecks}, {d.ExecutorAmountGross, &s.ExecutorAmountKopecks}} {
		if pair.in != nil {
			value, err := requiredMoney(pair.in)
			if err != nil {
				return TaskSnapshot{}, err
			}
			*pair.out = &value
		}
	}
	for _, pair := range []struct {
		in  json.RawMessage
		out **bool
	}{{d.SignedAt, &s.CustomerSigned}, {d.ExecutorSignedAt, &s.ExecutorSigned}} {
		if len(pair.in) == 0 {
			continue
		}
		signed := string(pair.in) != "null"
		if signed {
			var at string
			if json.Unmarshal(pair.in, &at) != nil {
				return TaskSnapshot{}, ErrFormat
			}
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				return TaskSnapshot{}, ErrFormat
			}
		}
		*pair.out = &signed
	}
	return s, nil
}

// CreateTask signs the customer agreement. Caller must persist its approval
// and checkpoint before invoking this method, including for automatic flags.
func (c *Client) CreateTask(ctx context.Context, t TaskTerms) (TaskSnapshot, error) {
	if err := t.validate(); err != nil {
		return TaskSnapshot{}, err
	}
	var response struct {
		Task *taskDTO `json:"task"`
	}
	if err := c.write(ctx, "CreateTask", http.MethodPost, "/api/tasks", t.wire(), &response); err != nil {
		return TaskSnapshot{}, err
	}
	if response.Task == nil {
		return TaskSnapshot{}, ErrUncertain
	}
	s, err := response.Task.snapshot()
	if err != nil || s.Type != "OneTime" || s.ExternalID != t.ExternalID || s.AmountKopecks != t.AmountKopecks || s.ExecutorINN != t.ExecutorINN || s.EmploymentKind != t.EmploymentKind {
		return TaskSnapshot{}, ErrUncertain
	}
	return s, nil
}

func (c *Client) GetTask(ctx context.Context, id int64) (TaskSnapshot, error) {
	if id <= 0 {
		return TaskSnapshot{}, ErrFormat
	}
	var response struct {
		Task *taskDTO `json:"task"`
	}
	if err := c.read(ctx, "GetTask", "/api/tasks/"+strconv.FormatInt(id, 10), nil, &response); err != nil {
		return TaskSnapshot{}, err
	}
	if response.Task == nil || response.Task.ID != id {
		return TaskSnapshot{}, ErrFormat
	}
	return response.Task.snapshot()
}
