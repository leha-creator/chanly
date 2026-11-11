package bootstrap

import (
	"context"
	"net/http"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
)

type bankTestSource struct{ in billingdomain.Incoming }

func (s bankTestSource) ReadPage(context.Context, billingdomain.BankQuery) (billingdomain.BankPage, error) {
	return billingdomain.BankPage{Incoming: []billingdomain.Incoming{s.in}, Operations: 1}, nil
}

func TestTBankControlledSmallInvoiceHTTP(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	adv := a.newAdvertiser("controlled-bank@example.ru")
	org := adv.orgID(t)
	other := a.newAdvertiser("ordinary-bank@example.ru")
	if res := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 10000}); res.status != http.StatusUnprocessableEntity {
		t.Fatalf("ordinary minimum bypass %d", res.status)
	}
	svc := a.mods.billing
	date := a.clk.Now()
	svc.BankConfig = billingapp.BankConfig{Access: billingdomain.BankControlled, Start: date.Add(-time.Hour), Period: 24 * time.Hour, Overlap: 24 * time.Hour, StaleAfter: time.Hour, DevRealEnabled: true, DevPaymentCode: "dev-server"}
	var inv invoiceView
	issued := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 10000})
	issued.json(t, &inv)
	var paymentView struct {
		PaymentDetails *struct {
			Recipient string `json:"recipient"`
			Account   string `json:"account"`
			Purpose   string `json:"purpose"`
		} `json:"payment_details"`
	}
	issued.json(t, &paymentView)
	if paymentView.PaymentDetails == nil || paymentView.PaymentDetails.Recipient != svc.Config.Supplier.Name || paymentView.PaymentDetails.Account != svc.Config.Supplier.BankAccount || paymentView.PaymentDetails.Purpose != "Пополнение баланса по счёту № "+inv.Number+". "+svc.BankConfig.PaymentMarker() {
		t.Fatalf("missing or incorrect invoice payment details: %+v", paymentView)
	}
	for _, amount := range []int64{99, int64(billingdomain.MaxInvoiceAmount) + 1} {
		if res := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": amount}); res.status != http.StatusUnprocessableEntity {
			t.Fatalf("amount accepted %d: %d", amount, res.status)
		}
	}
	if res := other.do(http.MethodPost, "/invoices", map[string]any{"amount": 10000}); res.status != http.StatusCreated {
		t.Fatalf("dev invoice rejected for second organization: %d", res.status)
	}
	var inn string
	if err := a.db.Pool.QueryRow(ctx, "SELECT inn FROM organization WHERE id=$1", org).Scan(&inn); err != nil {
		t.Fatal(err)
	}
	svc.BankSource = bankTestSource{in: billingdomain.Incoming{ExternalID: "controlled-small", BankOperationID: "controlled-small", DocumentNumber: "1", OperationDate: date, DocDate: &date, ChargeDate: &date, Amount: 10000, PayerINN: inn, PayerAccount: "40702810500000000004", Purpose: "Оплата по счету " + inv.Number + " " + svc.BankConfig.PaymentMarker()}}
	if res, err := svc.SyncBank(ctx); err != nil || res.Matched != 1 {
		t.Fatalf("controlled sync %+v %v", res, err)
	}
	var balance struct {
		Available int64 `json:"available_amount"`
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &balance)
	if balance.Available != 10000 {
		t.Fatalf("small deposit %d", balance.Available)
	}
}

func TestTBankAutomaticDepositEndToEnd(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	adv := a.newAdvertiser("bank-api-e2e@example.ru")
	org := adv.orgID(t)
	var inv invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 500000}).json(t, &inv)
	var inn string
	if err := a.db.Pool.QueryRow(ctx, "SELECT inn FROM organization WHERE id=$1", org).Scan(&inn); err != nil {
		t.Fatal(err)
	}
	date := a.clk.Now().Add(-time.Hour)
	svc := a.mods.billing
	svc.BankConfig = billingapp.BankConfig{Access: billingdomain.BankAutomation, Start: date.Add(-time.Hour), Period: 24 * time.Hour, Overlap: 24 * time.Hour, StaleAfter: time.Hour}
	in := billingdomain.Incoming{ExternalID: "tbank:e2e-op", BankOperationID: "first", DocumentNumber: "17", DocDate: &date, ChargeDate: &date, OperationDate: date, Amount: 500000, PayerINN: inn, PayerAccount: "40702810500000000004", Purpose: "Пополнение по счету " + inv.Number}
	svc.BankSource = bankTestSource{in: in}
	if res, err := svc.SyncBank(ctx); err != nil || res.Matched != 1 {
		t.Fatalf("sync %+v %v", res, err)
	}
	in.ExternalID = "tbank:e2e-changed"
	in.BankOperationID = "changed"
	svc.BankSource = bankTestSource{in: in}
	a.clk.Advance(time.Minute)
	if _, err := svc.SyncBank(ctx); err != nil {
		t.Fatal(err)
	}
	var balance struct {
		Available int64 `json:"available_amount"`
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &balance)
	if balance.Available != 500000 {
		t.Fatalf("balance %d", balance.Available)
	}
	var status string
	if err := a.db.Pool.QueryRow(ctx, "SELECT status FROM invoice WHERE number=$1", inv.Number).Scan(&status); err != nil || status != "paid" {
		t.Fatalf("invoice %s %v", status, err)
	}
	for _, q := range []string{"SELECT count(*) FROM ledger_transaction WHERE kind='deposit'", "SELECT count(*) FROM notification WHERE template='balance.deposited' AND channel='cabinet'"} {
		var n int
		if err := a.db.Pool.QueryRow(ctx, q).Scan(&n); err != nil || n != 1 {
			t.Fatalf("count %d %v", n, err)
		}
	}
	senior := a.adminClient("bank-e2e-senior@example.ru", accountdomain.AdminSenior)
	var summary struct {
		Source     string    `json:"source"`
		UploadedBy *struct{} `json:"uploaded_by"`
	}
	senior.do(http.MethodGet, "/admin/bank-statements/latest", nil).json(t, &summary)
	if summary.Source != "tbank" || summary.UploadedBy != nil {
		t.Fatalf("system summary %+v", summary)
	}
}
