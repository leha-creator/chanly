package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestBankInvoiceAgentContent(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "controlled"}[controlled], func(t *testing.T) {
			e := newEnv(t)
			// The bank and the printed date must use the same Moscow day.
			e.clk.Set(time.Date(2026, 11, 17, 21, 30, 0, 0, time.UTC))
			f := &fakeInvoiceIssuer{}
			enableInvoiceIssuer(e, f)
			if controlled {
				e.svc.BankConfig = app.BankConfig{Access: domain.BankControlled, DevRealEnabled: true, DevPaymentCode: strings.Repeat("A", 48)}
			}
			// Exercise the longest number accepted by T-API as well as the longest dev code.
			if _, err := e.d.Owner.Exec(context.Background(), "SELECT setval('invoice_number_seq', 999999999999998)"); err != nil {
				t.Fatal(err)
			}
			inv := e.invoice(100000)
			wantPurpose := "Средства принципала на исполнение поручения по оферте Chanly. Счёт № " + inv.Number + " от 18.11.2026. Без НДС"
			if controlled {
				wantPurpose += ". " + e.svc.BankConfig.PaymentMarker()
			}
			if f.doc.Description != wantPurpose || f.doc.PaymentPurpose != wantPurpose || inv.PaymentDetails.Purpose != wantPurpose {
				t.Fatalf("inconsistent agent invoice: %+v, %+v", f.doc, inv.PaymentDetails)
			}
			if utf8.RuneCountInString(wantPurpose) > 210 {
				t.Fatalf("payment purpose too long: %d", utf8.RuneCountInString(wantPurpose))
			}
			if !strings.HasPrefix(f.doc.Comment, "Основание: оферта рекламодателя https://chanly.ru/offer\n") || !strings.Contains(f.doc.Comment, "Назначение платежа: "+wantPurpose) || strings.Contains(f.doc.Comment, "100%") {
				t.Fatalf("wrong invoice comment: %q", f.doc.Comment)
			}
			var snapshot []byte
			if err := e.d.Pool.QueryRow(context.Background(), "SELECT request_snapshot FROM bank_invoice_document WHERE invoice_id=$1", inv.ID).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			var stored domain.InvoiceDoc
			if err := json.Unmarshal(snapshot, &stored); err != nil || stored.Description != f.doc.Description || stored.Comment != f.doc.Comment || stored.PaymentPurpose != wantPurpose {
				t.Fatalf("invoice content snapshot lost: %+v, %v", stored, err)
			}
		})
	}
}

type fakeInvoiceIssuer struct {
	sends, downloads     int
	sendErr, downloadErr error
	doc                  domain.InvoiceDoc
}

func (f *fakeInvoiceIssuer) Send(_ context.Context, doc domain.InvoiceDoc) (domain.BankInvoiceDocument, error) {
	f.sends++
	f.doc = doc
	return domain.BankInvoiceDocument{ID: uuid.New(), PDFURL: "https://business.tbank.ru/pdf"}, f.sendErr
}
func (f *fakeInvoiceIssuer) Download(context.Context, string) ([]byte, error) {
	f.downloads++
	return []byte("%PDF-1.7 bank with QR"), f.downloadErr
}

func enableInvoiceIssuer(e *env, f *fakeInvoiceIssuer) {
	e.svc.Config.InvoiceProvider = "tbank"
	e.svc.InvoiceDocuments = postgres.New(e.d.Pool, logx.Discard())
	e.svc.InvoiceIssuer = f
}

func TestBankInvoiceRollbackKeepsDocuments(t *testing.T) {
	e := newEnv(t)
	old := e.invoice(100000)
	f := &fakeInvoiceIssuer{}
	enableInvoiceIssuer(e, f)
	inv := e.invoice(100001)
	if f.sends != 1 || f.doc.PaymentPurpose != inv.PaymentDetails.Purpose || e.balance(e.org) != 0 {
		t.Fatal("wrong issuance or unexpected credit")
	}
	if string(e.storedPDF(inv.PDFStorageKey)) != "%PDF-1.7 bank with QR" {
		t.Fatal("bank PDF not archived")
	}
	e.svc.Config.InvoiceProvider = "local"
	e.svc.InvoiceIssuer = nil
	for _, id := range []uuid.UUID{old.ID, inv.ID} {
		_, r, _, err := e.svc.InvoicePDF(context.Background(), e.org, id)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	e.invoice(100000)
	if f.sends != 1 {
		t.Fatal("local rollback contacted bank")
	}
}

func TestBankInvoiceUnknownOutcomeBlocksReplay(t *testing.T) {
	e := newEnv(t)
	f := &fakeInvoiceIssuer{sendErr: context.DeadlineExceeded}
	enableInvoiceIssuer(e, f)
	_, err := e.svc.CreateInvoice(e.as(e.user, "192.0.2.1"), e.org, e.user, 100000, nil)
	if !isCode(err, apperr.CodeServiceUnavailable) {
		t.Fatalf("unknown outcome: %v", err)
	}
	_, err = e.svc.CreateInvoice(e.as(e.user, "192.0.2.1"), e.org, e.user, 100000, nil)
	if !isCode(err, apperr.CodeConflict) || f.sends != 1 {
		t.Fatalf("replayed unknown creation: %v, calls %d", err, f.sends)
	}
	if e.count("SELECT count(*) FROM invoice") != 1 {
		t.Fatal("failed reservation leaked a second invoice")
	}
	page, err := e.svc.ListInvoices(context.Background(), e.org, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("unresolved invoice lost")
	}
	_, _, _, err = e.svc.InvoicePDF(context.Background(), uuid.New(), page.Items[0].ID)
	if !isCode(err, apperr.CodeNotFound) {
		t.Fatal("foreign document lookup")
	}
	if f.downloads != 0 {
		t.Fatal("unknown document downloaded")
	}
}

func TestBankInvoiceDownloadRetryDoesNotRecreate(t *testing.T) {
	e := newEnv(t)
	f := &fakeInvoiceIssuer{downloadErr: errors.New("storage unavailable")}
	enableInvoiceIssuer(e, f)
	inv := e.invoice(100000)
	if f.sends != 1 {
		t.Fatal("not issued")
	}
	f.downloadErr = nil
	e.svc.Config.InvoiceProvider = "local"
	_, r, _, err := e.svc.InvoicePDF(context.Background(), e.org, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "%PDF-1.7 bank with QR" || f.sends != 1 || f.downloads != 2 {
		t.Fatal("download recreated invoice")
	}
}

func TestBankInvoiceBackgroundArchiveAfterRollback(t *testing.T) {
	e := newEnv(t)
	f := &fakeInvoiceIssuer{downloadErr: errors.New("temporary outage")}
	enableInvoiceIssuer(e, f)
	inv := e.invoice(100000)
	e.svc.Config.InvoiceProvider = "local"
	if err := e.svc.ArchiveBankInvoiceDocuments(context.Background()); err == nil {
		t.Fatal("download failure hidden")
	}
	f.downloadErr = nil
	if err := e.svc.ArchiveBankInvoiceDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.sends != 1 || string(e.storedPDF(inv.PDFStorageKey)) != "%PDF-1.7 bank with QR" {
		t.Fatal("archive did not preserve issued document")
	}
	downloads := f.downloads
	if err := e.svc.ArchiveBankInvoiceDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.downloads != downloads {
		t.Fatal("archived PDF downloaded again")
	}
}
