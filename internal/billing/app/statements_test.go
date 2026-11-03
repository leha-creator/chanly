package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// payment — поступление в выписке 1С.
type payment struct {
	number  int
	amount  money.Kopecks
	inn     string
	purpose string
}

// statementFile — выписка 1С в UTF-8 по счёту account; header меняет файл без изменения операций.
func statementFile(account, header string, payments ...payment) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "1CClientBankExchange\nВерсияФормата=1.03\nКодировка=UTF-8\nОтправитель=%s\n", header)
	fmt.Fprintf(&b, "ДатаНачала=19.10.2026\nДатаКонца=19.10.2026\nРасчСчет=%s\n", account)
	fmt.Fprintf(&b, "СекцияРасчСчет\nРасчСчет=%s\nНачальныйОстаток=0.00\nКонечныйОстаток=0.00\nКонецРасчСчет\n", account)
	for _, p := range payments {
		fmt.Fprintf(&b, "СекцияДокумент=Платёжное поручение\nНомер=%d\nДата=19.10.2026\nСумма=%d.%02d\n",
			p.number, p.amount/100, p.amount%100)
		fmt.Fprintf(&b, "ПлательщикСчет=40702810500000000004\nПлательщикИНН=%s\nПлательщик=ООО Плательщик\n", p.inn)
		fmt.Fprintf(&b, "ПолучательСчет=%s\nДатаПоступило=19.10.2026\nНазначениеПлатежа=%s\nКонецДокумента\n", account, p.purpose)
	}
	b.WriteString("КонецФайла\n")
	return []byte(b.String())
}

func (e *env) importStatement(data []byte) app.ImportResult {
	e.t.Helper()
	res, err := e.svc.ImportStatement(context.Background(), e.admin, data)
	if err != nil {
		e.t.Fatalf("import: %v", err)
	}
	return res
}

func (e *env) invoiceStatus(id uuid.UUID) (domain.InvoiceStatus, *money.Kopecks) {
	e.t.Helper()
	inv, err := e.svc.Invoices.Get(context.Background(), e.org, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return inv.Status, inv.PaidAmount
}

// unmatchedID — единственное несопоставленное поступление.
func (e *env) unmatchedID() uuid.UUID {
	e.t.Helper()
	page, err := e.svc.ListBankTransactions(context.Background(), domain.MatchUnmatched, "", 10)
	if err != nil || len(page.Items) != 1 {
		e.t.Fatalf("unmatched: %+v, %v", page, err)
	}
	return page.Items[0].ID
}

func TestImportMatchesByNumber(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(5_000_000)
	other := e.invoice(4_000_000)
	res := e.importStatement(statementFile(platformAccount, "Сбербанк",
		payment{1, 5_000_000, "7707083893", "Пополнение баланса по счёту № " + inv.Number},
		payment{2, 3_500_000, "7707083893", "оплата по счету " + strings.ToLower(other.Number)}))
	if res.Incoming != 2 || res.Matched != 2 || res.Unmatched != 0 || res.Duplicates != 0 || res.Credited != 8_500_000 {
		t.Fatalf("result %+v", res)
	}
	if st, paid := e.invoiceStatus(inv.ID); st != domain.InvoicePaid || *paid != 5_000_000 {
		t.Fatalf("invoice %s %v", st, paid)
	}
	// Сумма меньше счёта: зачислена фактическая.
	if st, paid := e.invoiceStatus(other.ID); st != domain.InvoicePaid || *paid != 3_500_000 {
		t.Fatalf("partial invoice %s %v", st, paid)
	}
	if got := e.balance(e.org); got != 8_500_000 {
		t.Fatalf("balance %d", got)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'bank_statement.imported' AND object_id = $1`, res.StatementID.String()) != 1 {
		t.Fatal("audit entry is missing")
	}
	if e.count(`SELECT count(*) FROM bank_statement WHERE matched_count = 2 AND incoming_count = 2`) != 1 {
		t.Fatal("statement counters")
	}
	if len(e.storedPDF("statements/"+res.StatementID.String())) == 0 {
		t.Fatal("statement file is not stored")
	}
	e.converges()
}

func TestImportMatchesByINNAndAmount(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(2_000_000)
	res := e.importStatement(statementFile(platformAccount, "Сбербанк",
		payment{1, 2_000_000, inv.Payer.INN, "Пополнение баланса, без НДС"}))
	if res.Matched != 1 {
		t.Fatalf("result %+v", res)
	}
	if st, _ := e.invoiceStatus(inv.ID); st != domain.InvoicePaid {
		t.Fatalf("invoice %s", st)
	}
	e.converges()
}

func TestImportTwoCandidatesUnmatched(t *testing.T) {
	e := newEnv(t)
	a, b := e.invoice(2_000_000), e.invoice(2_000_000)
	res := e.importStatement(statementFile(platformAccount, "Сбербанк",
		payment{1, 2_000_000, a.Payer.INN, "Пополнение баланса"}))
	if res.Matched != 0 || res.Unmatched != 1 {
		t.Fatalf("result %+v", res)
	}
	id := e.unmatchedID()
	if !e.alerts.open[app.ReasonUnmatched+":"+id.String()] {
		t.Fatal("unmatched alert is not raised")
	}
	if e.balance(e.org) != 0 {
		t.Fatal("unmatched payment credited")
	}
	for _, inv := range []domain.Invoice{a, b} {
		if st, _ := e.invoiceStatus(inv.ID); st != domain.InvoiceIssued {
			t.Fatalf("invoice %s", st)
		}
	}
	e.converges()
}

func TestImportExpiredInvoiceByNumber(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(1_000_000)
	e.clk.Advance(15 * 24 * time.Hour)
	if n, err := e.svc.ExpireInvoices(context.Background()); err != nil || n != 1 {
		t.Fatalf("expire: %d, %v", n, err)
	}
	res := e.importStatement(statementFile(platformAccount, "Сбербанк", payment{1, 1_000_000, "7707083893", inv.Number}))
	if res.Matched != 1 {
		t.Fatalf("result %+v", res)
	}
	if st, _ := e.invoiceStatus(inv.ID); st != domain.InvoicePaid {
		t.Fatalf("invoice %s", st)
	}
}

func TestImportRepeats(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(1_000_000)
	op := payment{7, 1_000_000, "7707083893", "по счёту " + inv.Number}
	file := statementFile(platformAccount, "Сбербанк", op)
	e.importStatement(file)
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, file); !errors.Is(err, domain.ErrStatementImported) ||
		!app.IsStatementRejection(err) {
		t.Fatalf("same file: %v", err)
	}
	// Другой файл (выписка за следующий день) с той же операцией: повтор пропущен.
	extra := payment{8, 300_000, "7707083893", "возврат обеспечения"}
	res := e.importStatement(statementFile(platformAccount, "Сбербанк (повтор)", op, extra))
	if res.Duplicates != 1 || res.Unmatched != 1 || res.Matched != 0 {
		t.Fatalf("result %+v", res)
	}
	if got := e.balance(e.org); got != 1_000_000 {
		t.Fatalf("balance %d: credited twice", got)
	}
	e.converges()
}

func TestImportRejectsForeignAccountAndBadFile(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.ImportStatement(context.Background(), e.admin,
		statementFile("40702810500000000004", "x", payment{1, 100_000, "7707083893", "x"}))
	if !errors.Is(err, domain.ErrStatementAccount) {
		t.Fatalf("foreign account: %v", err)
	}
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, []byte("not a statement")); !errors.Is(err, domain.ErrStatementFormat) {
		t.Fatalf("bad file: %v", err)
	}
	if e.count(`SELECT count(*) FROM bank_statement`) != 0 {
		t.Fatal("rejected statement saved")
	}
}

func TestMatchManually(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(2_000_000)
	e.importStatement(statementFile(platformAccount, "Сбербанк", payment{1, 1_999_000, "7707083893", "за рекламу"}))
	id := e.unmatchedID()
	ctx := context.Background()
	if _, err := e.svc.MatchManually(ctx, id, e.org, inv.ID, e.admin, ""); !isCode(err, apperr.CodeValidationFailed) {
		t.Fatalf("no reason: %v", err)
	}
	stranger := uuid.New()
	if _, err := e.svc.MatchManually(ctx, id, stranger, uuid.Nil, e.admin, "x"); !isCode(err, apperr.CodeNotFound) {
		t.Fatalf("unknown organization: %v", err)
	}
	bt, err := e.svc.MatchManually(ctx, id, e.org, inv.ID, e.admin, "письмо плательщика")
	if err != nil || bt.MatchStatus != domain.MatchMatched || bt.MatchedBy != e.admin || bt.InvoiceID != inv.ID {
		t.Fatalf("match: %+v, %v", bt, err)
	}
	if got := e.balance(e.org); got != 1_999_000 {
		t.Fatalf("balance %d", got)
	}
	if st, paid := e.invoiceStatus(inv.ID); st != domain.InvoicePaid || *paid != 1_999_000 {
		t.Fatalf("invoice %s %v", st, paid)
	}
	if e.alerts.open[app.ReasonUnmatched+":"+id.String()] {
		t.Fatal("alert is not resolved")
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'bank_transaction.matched' AND object_id = $1`, id.String()) != 1 {
		t.Fatal("audit entry is missing")
	}
	if _, err := e.svc.MatchManually(ctx, id, e.org, uuid.Nil, e.admin, "ещё раз"); !isCode(err, apperr.CodeConflict) {
		t.Fatalf("match twice: %v", err)
	}
	e.converges()
}

func TestIgnoredThenMatched(t *testing.T) {
	e := newEnv(t)
	e.importStatement(statementFile(platformAccount, "Сбербанк", payment{1, 500_000, "7707083893", "возврат"}))
	id := e.unmatchedID()
	ctx := context.Background()
	bt, err := e.svc.Ignore(ctx, id, e.admin, "возврат поставщика")
	if err != nil || bt.MatchStatus != domain.MatchIgnored || bt.IgnoredReason != "возврат поставщика" {
		t.Fatalf("ignore: %+v, %v", bt, err)
	}
	if _, err := e.svc.Ignore(ctx, id, e.admin, "ещё раз"); !isCode(err, apperr.CodeConflict) {
		t.Fatalf("ignore twice: %v", err)
	}
	// Без счёта: зачисление на баланс организации.
	bt, err = e.svc.MatchManually(ctx, id, e.org, uuid.Nil, e.admin, "ошибочно пропущено")
	if err != nil || bt.MatchStatus != domain.MatchMatched || bt.IgnoredReason != "" {
		t.Fatalf("match ignored: %+v, %v", bt, err)
	}
	if got := e.balance(e.org); got != 500_000 {
		t.Fatalf("balance %d", got)
	}
	page, err := e.svc.ListBankTransactions(ctx, "", "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %+v, %v", page, err)
	}
	if _, err := e.svc.ListBankTransactions(ctx, "bogus", "", 10); !isCode(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad status: %v", err)
	}
	e.converges()
}

func TestBillingMetrics(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(1_000_000)
	e.invoice(2_000_000)
	e.importStatement(statementFile(platformAccount, "Сбербанк",
		payment{1, 1_000_000, "7707083893", inv.Number}, payment{2, 50_000, "7707083893", "x"}))
	reg := prometheus.NewRegistry()
	reg.MustRegister(postgres.NewCollector(postgres.New(e.d.Pool, logx.Discard())))
	want := `
# HELP maxim_bank_transactions Bank transactions by match status.
# TYPE maxim_bank_transactions gauge
maxim_bank_transactions{match_status="matched"} 1
maxim_bank_transactions{match_status="unmatched"} 1
# HELP maxim_invoices Invoices by status.
# TYPE maxim_invoices gauge
maxim_invoices{status="issued"} 1
maxim_invoices{status="paid"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "maxim_bank_transactions", "maxim_invoices"); err != nil {
		t.Fatal(err)
	}
}
