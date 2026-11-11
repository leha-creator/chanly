package app_test

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestCreateInvoice(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(5_000_000)
	if inv.Number != "MX-000001" || inv.Status != domain.InvoiceIssued || !inv.ExpiresAt.Equal(start.Add(14*24*time.Hour)) {
		t.Fatalf("invoice %+v", inv)
	}
	if inv.Payer.Name != "Рекламодатель" || inv.Payer.INN == "" || inv.PDFStorageKey != "invoices/"+inv.ID.String()+".pdf" {
		t.Fatalf("payer snapshot %+v", inv)
	}
	if !bytes.HasPrefix(e.storedPDF(inv.PDFStorageKey), []byte("%PDF")) {
		t.Fatal("pdf is not stored")
	}
	if inv.PaymentDetails == nil || inv.PaymentDetails.Account != e.svc.Config.Supplier.BankAccount || inv.PaymentDetails.Purpose != "Пополнение баланса по счёту № "+inv.Number {
		t.Fatalf("invoice payment details missing: %+v", inv.PaymentDetails)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'invoice.issued' AND object_id = $1`, inv.ID.String()) != 1 {
		t.Fatal("audit entry is missing")
	}
	second := e.invoice(100_000)
	if second.Number != "MX-000002" {
		t.Fatalf("second number %s", second.Number)
	}
	page, err := e.svc.ListInvoices(context.Background(), e.org, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor == "" {
		t.Fatalf("first page %+v, %v", page, err)
	}
	if page.Items[0].PaymentDetails != nil {
		t.Fatal("historical invoice must not use current supplier details")
	}
	page, err = e.svc.ListInvoices(context.Background(), e.org, page.NextCursor, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != inv.ID || page.NextCursor != "" {
		t.Fatalf("second page %+v, %v", page, err)
	}
	if _, err := e.svc.ListInvoices(context.Background(), e.org, "garbage", 1); !isCode(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func isCode(err error, code apperr.Code) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}

func TestCreateInvoiceChecks(t *testing.T) {
	e := newEnv(t)
	ip := netip.MustParseAddr("192.0.2.1")
	for _, amount := range []int64{99_999, 100_000_000_001} {
		if _, err := e.svc.CreateInvoice(e.as(e.user, "192.0.2.1"), e.org, e.user, money.Kopecks(amount), &ip); !isCode(err, apperr.CodeValidationFailed) {
			t.Fatalf("amount %d: %v", amount, err)
		}
	}
	for range 10 {
		e.invoice(100_000)
	}
	if _, err := e.svc.CreateInvoice(e.as(e.user, "192.0.2.1"), e.org, e.user, 100_000, &ip); !isCode(err, apperr.CodeRateLimited) {
		t.Fatalf("11th invoice: %v", err)
	}
	// Через час лимит снова свободен.
	e.clk.Advance(time.Hour + time.Second)
	e.invoice(100_000)
	if e.count(`SELECT count(*) FROM invoice`) != 11 {
		t.Fatal("invoices count")
	}
}

func TestInvoiceRateLimitByIP(t *testing.T) {
	e := newEnv(t)
	ip := netip.MustParseAddr("198.51.100.7")
	// Четыре пользователя по 8 счетов с одного адреса: лимит адреса (30) общий для всех.
	users := []uuid.UUID{e.user, ledgertest.User(t, e.d.Pool), ledgertest.User(t, e.d.Pool), ledgertest.User(t, e.d.Pool)}
	issued, limited := 0, 0
	for _, user := range users {
		for range 8 {
			_, err := e.svc.CreateInvoice(e.as(user, ip.String()), e.org, user, 100_000, &ip)
			switch {
			case err == nil:
				issued++
			case isCode(err, apperr.CodeRateLimited):
				limited++
			default:
				t.Fatal(err)
			}
		}
	}
	if issued != 30 || limited != 2 {
		t.Fatalf("issued %d limited %d", issued, limited)
	}
}

func TestInvoicePDFOwnOrganization(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(100_000)
	_, body, _, err := e.svc.InvoicePDF(context.Background(), e.org, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if _, _, _, err := e.svc.InvoicePDF(context.Background(), e.admin, inv.ID); !isCode(err, apperr.CodeNotFound) {
		t.Fatalf("foreign pdf: %v", err)
	}
}

func TestExpireInvoices(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(100_000)
	e.clk.Advance(14*24*time.Hour - time.Minute)
	if n, err := e.svc.ExpireInvoices(context.Background()); err != nil || n != 0 {
		t.Fatalf("before expiry: %d, %v", n, err)
	}
	e.clk.Advance(2 * time.Minute)
	if n, err := e.svc.ExpireInvoices(context.Background()); err != nil || n != 1 {
		t.Fatalf("expiry: %d, %v", n, err)
	}
	if n, err := e.svc.ExpireInvoices(context.Background()); err != nil || n != 0 {
		t.Fatalf("repeated expiry: %d, %v", n, err)
	}
	page, _ := e.svc.ListInvoices(context.Background(), e.org, "", 10)
	if page.Items[0].ID != inv.ID || page.Items[0].Status != domain.InvoiceExpired {
		t.Fatalf("status %s", page.Items[0].Status)
	}
}

func TestCancelInvoice(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(100_000)
	if _, err := e.svc.CancelInvoice(context.Background(), inv.ID, e.admin, ""); !isCode(err, apperr.CodeValidationFailed) {
		t.Fatalf("no reason: %v", err)
	}
	got, err := e.svc.CancelInvoice(context.Background(), inv.ID, e.admin, "дубль счёта")
	if err != nil || got.Status != domain.InvoiceCancelled || got.CancelReason != "дубль счёта" {
		t.Fatalf("cancel: %+v, %v", got, err)
	}
	if _, err := e.svc.CancelInvoice(context.Background(), inv.ID, e.admin, "ещё раз"); !isCode(err, apperr.CodeConflict) {
		t.Fatalf("cancel twice: %v", err)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'invoice.cancelled'`) != 1 {
		t.Fatal("audit")
	}
	if _, err := e.svc.CancelInvoice(context.Background(), e.org, e.admin, "x"); !isCode(err, apperr.CodeNotFound) {
		t.Fatalf("unknown invoice: %v", err)
	}
}
