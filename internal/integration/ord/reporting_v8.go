package ord

import (
	"context"
	"errors"
)

// StatisticsV8 is the API v8 batch body. A local correlation ID is deliberately
// not serialized: the provider identifies entries by their reporting dimensions.
type StatisticsV8 struct {
	Statistics []StatisticV8 `json:"statistics"`
}

type StatisticV8 struct {
	CreativeID    string       `json:"creativeId"`
	PlatformID    string       `json:"platformId"`
	DateStartFact string       `json:"dateStartFact"`
	DateEndFact   string       `json:"dateEndFact,omitempty"`
	DateStartPlan string       `json:"dateStartPlan,omitempty"`
	DateEndPlan   string       `json:"dateEndPlan,omitempty"`
	ImpsFact      *int64       `json:"impsFact,omitempty"`
	ImpsPlan      *int64       `json:"impsPlan,omitempty"`
	InvoiceID     string       `json:"invoiceId,omitempty"`
	ContractID    string       `json:"contractId,omitempty"`
	Type          string       `json:"type,omitempty"`
	AmountPerUnit string       `json:"amountPerUnit,omitempty"`
	Amount        *TaxAmountV8 `json:"amount,omitempty"`
	Irrelevant    *bool        `json:"irrelevant,omitempty"`
}

// TaxAmountV8 preserves explicit tax inputs, including zero values. It never
// infers a rate or commission from a gross amount. Rate "100" means no VAT;
// "0" is the distinct zero-percent rate. Strings preserve exact decimal money.
type TaxAmountV8 struct {
	ExcludingVAT string `json:"excludingVat"`
	VATRate      string `json:"vatRate,omitempty"`
	VAT          string `json:"vat"`
	IncludingVAT string `json:"includingVat"`
}

type InvoiceV8 struct {
	ID                 string                `json:"id"`
	ContractID         string                `json:"contractId,omitempty"`
	ClientRole         string                `json:"clientRole,omitempty"`
	ContractorRole     string                `json:"contractorRole,omitempty"`
	Date               string                `json:"date,omitempty"`
	StartDate          string                `json:"startDate,omitempty"`
	EndDate            string                `json:"endDate,omitempty"`
	Type               string                `json:"type,omitempty"`
	Number             string                `json:"number,omitempty"`
	Amount             *InvoiceAmountV8      `json:"amount,omitempty"`
	Items              []InvoiceAllocationV8 `json:"items,omitempty"`
	IsSocial           *bool                 `json:"isSocial,omitempty"`
	IsSocialQuota      *bool                 `json:"isSocialQuota,omitempty"`
	Irrelevant         *bool                 `json:"irrelevant,omitempty"`
	RROrderContractIDs []string              `json:"rrOrderContractIds,omitempty"`
}

type InvoiceAmountV8 struct {
	Services   InvoiceServicesV8    `json:"services"`
	Commission *InvoiceCommissionV8 `json:"commission,omitempty"`
}

// A commission-invoice references intermediary reports instead of service
// totals. Embedded pointer fields preserve this omission in the wire body.
type InvoiceServicesV8 struct {
	*TaxAmountV8
	IntermediaryReportIDs []string `json:"intermediaryReportIdList,omitempty"`
}

type InvoiceCommissionV8 struct {
	*TaxAmountV8
	IsInvoice *bool `json:"isInvoice,omitempty"`
}

// ContractID is an INITIAL contract, not the publisher contract or a platform.
type InvoiceAllocationV8 struct {
	ContractID    string      `json:"contractId"`
	Amount        TaxAmountV8 `json:"amount"`
	IsSocial      *bool       `json:"isSocial,omitempty"`
	IsSocialQuota *bool       `json:"isSocialQuota,omitempty"`
}

type InvoiceCreativesV8 struct {
	Creatives []InvoiceCreativeLinksV8 `json:"creatives"`
}

type InvoiceCreativeLinksV8 struct {
	InvoiceID   string   `json:"invoiceId"`
	ContractID  string   `json:"contractId"`
	CreativeIDs []string `json:"creativeIds"`
	Irrelevant  *bool    `json:"irrelevant,omitempty"`
}

// Each method performs exactly one submission and returns its own receipt.
// Durable workflow checkpoints and polling belong to the caller. These methods
// do not adapt the legacy report domain or choose financial/legal parameters.
func (c *Client) SubmitStatisticsV8(ctx context.Context, correlationID string, payload StatisticsV8) (Receipt, error) {
	if correlationID == "" || len(payload.Statistics) == 0 {
		return Receipt{}, errors.New("ORD statistics require correlation ID and entries")
	}
	return c.submitWithPolicy(ctx, OpStatistics, correlationID, payload, true)
}

func (c *Client) SubmitInvoiceV8(ctx context.Context, payload InvoiceV8) (Receipt, error) {
	if payload.ID == "" {
		return Receipt{}, errors.New("ORD invoice requires ID")
	}
	return c.submitWithPolicy(ctx, OpInvoice, payload.ID, payload, true)
}

func (c *Client) SubmitInvoiceCreativesV8(ctx context.Context, correlationID string, payload InvoiceCreativesV8) (Receipt, error) {
	if correlationID == "" || len(payload.Creatives) == 0 {
		return Receipt{}, errors.New("ORD invoice creatives require correlation ID and entries")
	}
	return c.submitWithPolicy(ctx, OpInvoiceCreatives, correlationID, payload, true)
}
