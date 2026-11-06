package bridge

import (
	"context"
	"reflect"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/domain"
)

type reportCapture struct {
	OrdClient
	invoice         ord.InvoiceV8
	links           ord.InvoiceCreativesV8
	statistics      ord.StatisticsV8
	docs, linkCalls int
}

func (c *reportCapture) SubmitInvoiceV8(_ context.Context, r ord.InvoiceV8) (ord.Receipt, error) {
	c.invoice = r
	c.docs++
	return ord.Receipt{RequestID: "document-receipt"}, nil
}
func (c *reportCapture) SubmitInvoiceCreativesV8(_ context.Context, _ string, r ord.InvoiceCreativesV8) (ord.Receipt, error) {
	c.links = r
	c.linkCalls++
	return ord.Receipt{RequestID: "links-receipt"}, nil
}
func (c *reportCapture) SubmitStatisticsV8(_ context.Context, _ string, r ord.StatisticsV8) (ord.Receipt, error) {
	c.statistics = r
	return ord.Receipt{}, nil
}

func TestReportMappingUsesSnapshotsAndInitialContracts(t *testing.T) {
	c := &reportCapture{}
	b := MarkingOrd{Client: c}
	tax := func(n int64) domain.TaxTotal {
		v, err := domain.GrossTax(n, "vat_20")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	req := domain.InvoiceRequest{ID: "invoice", ContractID: "publisher-contract", Type: "intermediary-report", ClientRole: "rd", ContractorRole: "psr",
		Finance: domain.ReportFinance{Services: tax(36000), Commission: tax(6000)},
		Items: []domain.InvoiceRequestItem{
			{ContractID: "initial-b", CreativeID: "creative-2", Tax: tax(12000)},
			{ContractID: "initial-a", CreativeID: "creative-1", Tax: tax(12000)},
			{ContractID: "initial-a", CreativeID: "creative-1", Tax: tax(12000)},
		}}
	rec, err := b.UpsertInvoice(context.Background(), req)
	if err != nil || rec.RequestID != "document-receipt" || c.docs != 1 || c.linkCalls != 0 {
		t.Fatalf("document: %+v %v", rec, err)
	}
	v := c.invoice
	if v.Amount.Services.IncludingVAT != "360.00" || v.Amount.Services.VAT != "60.00" || v.Amount.Commission.IncludingVAT != "60.00" || v.Amount.Commission.VAT != "10.00" {
		t.Fatalf("amounts: %+v", v.Amount)
	}
	if len(v.Items) != 2 || v.Items[0].ContractID != "initial-a" || v.Items[0].Amount.IncludingVAT != "240.00" || v.Items[1].ContractID != "initial-b" {
		t.Fatalf("allocations: %+v", v.Items)
	}
	rec, err = b.UpsertInvoiceCreatives(context.Background(), req)
	if err != nil || rec.RequestID != "links-receipt" || c.docs != 1 || c.linkCalls != 1 {
		t.Fatalf("links: %+v %v", rec, err)
	}
	if len(c.links.Creatives) != 2 || !reflect.DeepEqual(c.links.Creatives[0].CreativeIDs, []string{"creative-1"}) {
		t.Fatalf("deduplication: %+v", c.links)
	}
}

func TestStatisticsMissingAndMixedVATRejectedBeforeNetwork(t *testing.T) {
	for _, snapshot := range []domain.TaxTotal{{}, {Known: true, Gross: 100, Rate: ""}} {
		b := MarkingOrd{} // nil client proves validation precedes network access
		if _, err := b.UpsertStatistics(context.Background(), domain.StatisticsRequest{Finance: domain.ReportFinance{Services: snapshot}}); err == nil {
			t.Fatal("ambiguous VAT accepted")
		}
	}
	c := &reportCapture{}
	b := MarkingOrd{Client: c}
	tax, _ := domain.GrossTax(0, "none")
	if _, err := b.UpsertStatistics(context.Background(), domain.StatisticsRequest{Amount: "0.00", Finance: domain.ReportFinance{Services: tax}}); err != nil {
		t.Fatal(err)
	}
	row := c.statistics.Statistics[0]
	if row.ImpsFact == nil || *row.ImpsFact != 0 || row.Amount == nil || row.Amount.IncludingVAT != "0.00" || row.Amount.VATRate != "100" {
		t.Fatal("explicit zeros or no-VAT lost")
	}
}
