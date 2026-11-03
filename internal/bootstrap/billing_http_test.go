package bootstrap

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

type invoiceView struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	PdfURL    string `json:"pdf_url"`
}

func TestInvoicesHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-invoice@example.ru")

	var inv invoiceView
	r := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 5_000_000})
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	r.json(t, &inv)
	if inv.Number != "MX-000001" || inv.Status != "issued" || inv.PdfURL != BaseURL+"/invoices/"+inv.ID+"/pdf" {
		t.Fatalf("invoice %+v", inv)
	}
	pdf := adv.do(http.MethodGet, "/invoices/"+inv.ID+"/pdf", nil)
	if pdf.status != http.StatusOK || !bytes.HasPrefix(pdf.body, []byte("%PDF")) ||
		pdf.header.Get("Content-Type") != "application/pdf" ||
		!strings.Contains(pdf.header.Get("Content-Disposition"), `filename="MX-000001.pdf"`) {
		t.Fatalf("pdf: %d %v", pdf.status, pdf.header)
	}
	var list struct {
		Items []invoiceView `json:"items"`
	}
	adv.do(http.MethodGet, "/invoices", nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != inv.ID {
		t.Fatalf("list %+v", list)
	}

	// Сумма меньше 1 000 ₽ — 422 (схема OpenAPI).
	if r := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 99_999}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("small amount: %d %s", r.status, r.body)
	}
	// Блогер без кабинета рекламодателя — 403.
	blg := a.bloggerClient("blg-invoice@example.ru")
	if r := blg.do(http.MethodPost, "/invoices", map[string]any{"amount": 100_000}); r.status != http.StatusForbidden {
		t.Fatalf("blogger: %d %s", r.status, r.body)
	}
	// Чужой PDF — 404.
	other := a.advertiserClient("adv-other-invoice@example.ru")
	if r := other.do(http.MethodGet, "/invoices/"+inv.ID+"/pdf", nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign pdf: %d", r.status)
	}
	// 11-й счёт за час — 429.
	for range 9 {
		if r := adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 100_000}); r.status != http.StatusCreated {
			t.Fatalf("create: %d %s", r.status, r.body)
		}
	}
	r = adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 100_000})
	if code, _ := r.errorCode(t); r.status != http.StatusTooManyRequests || code != "rate_limited" || r.header.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %s", r.status, r.body)
	}
}

// Закрывающие документы в кабинете (R-16, R-19): список, оба PDF, чужой — 404.
func TestClosingActsHTTP(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	adv := a.newAdvertiser("adv-closing@example.ru")
	org := adv.orgID(t)
	period, _ := billingdomain.ParsePeriod("2026-09")
	act := billingdomain.ClosingAct{ID: ids.New(), OrganizationID: org, Period: period, Number: "А-000007",
		PlacementsAmount: 1_000_000, FeeAmount: 150_000, ReportKey: "closing/test-report.pdf", ActKey: "closing/test-act.pdf",
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	for _, key := range []string{act.ReportKey, act.ActKey} {
		pdf := []byte("%PDF-1.4 " + key)
		if err := a.mods.in.storage.PutPrivate(ctx, key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.mods.billingStore.ClosingActs().Insert(ctx, act); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			ID               string `json:"id"`
			Number           string `json:"number"`
			Period           string `json:"period"`
			PlacementsAmount int64  `json:"placements_amount"`
			FeeAmount        int64  `json:"fee_amount"`
			ReportURL        string `json:"report_url"`
			ActURL           string `json:"act_url"`
		} `json:"items"`
	}
	if r := adv.do(http.MethodGet, "/closing-acts", nil); r.status != http.StatusOK {
		t.Fatalf("list: %d %s", r.status, r.body)
	} else {
		r.json(t, &list)
	}
	if len(list.Items) != 1 || list.Items[0].Number != "А-000007" || list.Items[0].Period != "2026-09" ||
		list.Items[0].FeeAmount != 150_000 || list.Items[0].ReportURL != BaseURL+"/closing-acts/"+act.ID.String()+"/report" {
		t.Fatalf("list %+v", list)
	}
	r := adv.do(http.MethodGet, "/closing-acts/"+act.ID.String()+"/act", nil)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "application/pdf" || !bytes.Contains(r.body, []byte("test-act")) ||
		!strings.Contains(r.header.Get("Content-Disposition"), `filename="closing-2026-09-act.pdf"`) {
		t.Fatalf("act pdf: %d %v %s", r.status, r.header, r.body)
	}
	if r := adv.do(http.MethodGet, "/closing-acts/"+act.ID.String()+"/report", nil); r.status != http.StatusOK || !bytes.Contains(r.body, []byte("test-report")) {
		t.Fatalf("report pdf: %d", r.status)
	}
	other := a.newAdvertiser("adv-closing-other@example.ru")
	if r := other.do(http.MethodGet, "/closing-acts/"+act.ID.String()+"/act", nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign act: %d", r.status)
	}
	var empty struct{ Items []any }
	other.do(http.MethodGet, "/closing-acts", nil).json(t, &empty)
	if len(empty.Items) != 0 {
		t.Fatalf("foreign list %+v", empty)
	}
	if r := adv.do(http.MethodGet, "/closing-acts/"+act.ID.String()+"/bogus", nil); r.status == http.StatusOK || r.status >= 500 {
		t.Fatalf("bad document: %d", r.status)
	}
}
