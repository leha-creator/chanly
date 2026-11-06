package bridge

import (
	"context"
	"sort"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func reportTax(t domain.TaxTotal) ord.TaxAmountV8 {
	return ord.TaxAmountV8{ExcludingVAT: domain.Rubles(t.Gross - t.VAT), IncludingVAT: domain.Rubles(t.Gross), VAT: domain.Rubles(t.VAT), VATRate: t.Rate}
}

func (b MarkingOrd) UpsertStatistics(ctx context.Context, r domain.StatisticsRequest) (domain.Receipt, error) {
	if !r.Finance.Services.Known || r.Finance.Services.Rate == "" {
		return domain.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"statistics require a known single VAT rate"}})
	}
	amount := reportTax(r.Finance.Services)
	body := ord.StatisticsV8{Statistics: []ord.StatisticV8{{CreativeID: r.CreativeID, PlatformID: r.PlatformID,
		DateStartFact: r.StartDateFact, DateEndFact: r.EndDateFact, DateStartPlan: r.StartDatePlan, DateEndPlan: r.EndDatePlan,
		ImpsFact: &r.ImpsFact, ImpsPlan: &r.ImpsPlan, Type: "other", AmountPerUnit: r.Amount, Amount: &amount}}}
	rec, err := b.Client.SubmitStatisticsV8(ctx, r.ID, body)
	return receipt(rec), err
}

// UpsertInvoice returns only the document receipt. Links have their own durable
// object/job and are never submitted from this method.
func (b MarkingOrd) UpsertInvoice(ctx context.Context, r domain.InvoiceRequest) (domain.Receipt, error) {
	if !r.Finance.Services.Known || r.ClientRole == "" || r.ContractorRole == "" {
		return domain.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"invoice financial snapshot or roles missing"}})
	}
	services := reportTax(r.Finance.Services)
	body := ord.InvoiceV8{ID: r.ID, ContractID: r.ContractID, Type: r.Type, Number: r.Number, Date: r.Date,
		StartDate: r.StartDate, EndDate: r.EndDate, ClientRole: r.ClientRole, ContractorRole: r.ContractorRole,
		Amount: &ord.InvoiceAmountV8{Services: ord.InvoiceServicesV8{TaxAmountV8: &services}}}
	if r.Type == "intermediary-report" {
		if !r.Finance.Commission.Known {
			return domain.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"invoice commission missing"}})
		}
		commission := reportTax(r.Finance.Commission)
		separate := false
		body.Amount.Commission = &ord.InvoiceCommissionV8{TaxAmountV8: &commission, IsInvoice: &separate}
	}
	allocations := map[string]domain.TaxTotal{}
	for _, item := range r.Items {
		if item.ContractID == "" || !item.Tax.Known {
			return domain.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"invoice allocation snapshot missing"}})
		}
		total := allocations[item.ContractID]
		total.Add(item.Tax)
		allocations[item.ContractID] = total
	}
	keys := make([]string, 0, len(allocations))
	for id := range allocations {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		body.Items = append(body.Items, ord.InvoiceAllocationV8{ContractID: id, Amount: reportTax(allocations[id])})
	}
	rec, err := b.Client.SubmitInvoiceV8(ctx, body)
	return receipt(rec), err
}

func (b MarkingOrd) UpsertInvoiceCreatives(ctx context.Context, r domain.InvoiceRequest) (domain.Receipt, error) {
	groups := map[string]map[string]bool{}
	for _, item := range r.Items {
		if item.ContractID == "" || item.CreativeID == "" {
			return domain.Receipt{}, job.Permanent(&ord.ValidationError{Details: []string{"invoice creative reference missing"}})
		}
		if groups[item.ContractID] == nil {
			groups[item.ContractID] = map[string]bool{}
		}
		groups[item.ContractID][item.CreativeID] = true
	}
	keys := make([]string, 0, len(groups))
	for id := range groups {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	body := ord.InvoiceCreativesV8{}
	for _, id := range keys {
		creatives := make([]string, 0, len(groups[id]))
		for cr := range groups[id] {
			creatives = append(creatives, cr)
		}
		sort.Strings(creatives)
		body.Creatives = append(body.Creatives, ord.InvoiceCreativeLinksV8{InvoiceID: r.ID, ContractID: id, CreativeIDs: creatives})
	}
	rec, err := b.Client.SubmitInvoiceCreativesV8(ctx, r.ID, body)
	return receipt(rec), err
}
