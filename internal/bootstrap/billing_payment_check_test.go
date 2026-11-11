package bootstrap

import (
	"context"
	"net/http"
	"testing"
	"time"

	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
)

func TestInvoicePaymentCheckHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("check-payment@example.ru")
	other := a.newAdvertiser("other-payment@example.ru")
	blg := a.bloggerClient("blg-check-payment@example.ru")
	var inv invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 500000}).json(t, &inv)
	path := "/invoices/" + inv.ID + "/check-payment"
	if r := other.do(http.MethodPost, path, nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign invoice: %d %s", r.status, r.body)
	}
	if r := blg.do(http.MethodPost, path, nil); r.status != http.StatusForbidden {
		t.Fatalf("cabinet check: %d %s", r.status, r.body)
	}
	var result struct {
		Invoice    invoiceView `json:"invoice"`
		Result     string      `json:"result"`
		CheckedAt  *time.Time  `json:"checked_at"`
		RetryAfter int         `json:"retry_after"`
	}
	adv.do(http.MethodPost, path, nil).json(t, &result)
	if result.Result != "unavailable" || result.Invoice.Status != "issued" {
		t.Fatalf("disabled bank: %+v", result)
	}
	svc := a.mods.billing
	svc.BankConfig = billingapp.BankConfig{Access: billingdomain.BankAutomation, Start: a.clk.Now().Add(-48 * time.Hour)}
	svc.BankSource = emptyBank{}
	adv.do(http.MethodPost, path, nil).json(t, &result)
	if result.Result != "not_found" || result.CheckedAt == nil {
		t.Fatalf("fresh check: %+v", result)
	}
	adv.do(http.MethodPost, path, nil).json(t, &result)
	if result.Result != "busy" || result.RetryAfter != 60 {
		t.Fatalf("rate gate: %+v", result)
	}
	a.clk.Advance(61 * time.Second)
	var inn string
	if err := a.db.Pool.QueryRow(context.Background(), "SELECT inn FROM organization WHERE id=$1", adv.orgID(t)).Scan(&inn); err != nil {
		t.Fatal(err)
	}
	date := a.clk.Now().Add(-time.Second)
	svc.BankSource = bankTestSource{in: billingdomain.Incoming{ExternalID: "check-http", BankOperationID: "check-http", DocumentNumber: "1", OperationDate: date, DocDate: &date, ChargeDate: &date, Amount: 500000, PayerINN: inn, PayerAccount: "40702810500000000004", Purpose: "Оплата по счету " + inv.Number}}
	adv.do(http.MethodPost, path, nil).json(t, &result)
	if result.Result != "paid" || result.Invoice.Status != "paid" {
		t.Fatalf("payment not reflected: %+v", result)
	}
	var balance struct {
		Available int64 `json:"available_amount"`
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &balance)
	if balance.Available != 500000 {
		t.Fatalf("balance: %d", balance.Available)
	}
}
