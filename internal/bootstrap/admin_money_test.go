package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
)

// testStatement — выписка 1С в UTF-8 по счёту платформы разработки: поступления «сумма в копейках,
// ИНН, назначение» и конечный остаток.
func testStatement(t *testing.T, closing int64, payments ...[3]string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "1CClientBankExchange\nВерсияФормата=1.03\nКодировка=UTF-8\nДатаНачала=19.10.2026\nДатаКонца=19.10.2026\n")
	fmt.Fprintf(&b, "РасчСчет=%s\nСекцияРасчСчет\nРасчСчет=%[1]s\nНачальныйОстаток=0.00\nКонечныйОстаток=%d.%02d\nКонецРасчСчет\n",
		devPlatformBankAccount, closing/100, closing%100)
	for i, p := range payments {
		fmt.Fprintf(&b, "СекцияДокумент=Платёжное поручение\nНомер=%d\nДата=19.10.2026\nСумма=%s\nПлательщикСчет=40702810500000000004\n", i+1, p[0])
		fmt.Fprintf(&b, "ПлательщикИНН=%s\nПлательщик=ООО Плательщик\nПолучательСчет=%s\nНазначениеПлатежа=%s\nКонецДокумента\n",
			p[1], devPlatformBankAccount, p[2])
	}
	b.WriteString("КонецФайла\n")
	path := filepath.Join(t.TempDir(), "statement.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Этап «Приём оплаты и учёт денег», R-13, R-14, 06 §2.4: maxim admin money.
func TestAdminMoney(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAdmin(ctx, cfg, a.mods.in.log, append([]string{"money"}, args...), &out)
		return out.String(), err
	}
	adv := a.newAdvertiser("adv.money@example.ru")
	org := adv.orgID(t)
	var inn string
	if err := a.db.Pool.QueryRow(ctx, `SELECT inn FROM organization WHERE id = $1`, org).Scan(&inn); err != nil {
		t.Fatal(err)
	}
	for email, role := range map[string]accountdomain.AdminRole{"senior1@example.ru": accountdomain.AdminSenior,
		"senior2@example.ru": accountdomain.AdminSenior, "moderator@example.ru": accountdomain.AdminModerator} {
		if _, err := a.mods.account.SetAdminRole(ctx, email, &role); err != nil {
			t.Fatal(err)
		}
	}
	var inv invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 2_000_000}).json(t, &inv)

	// Выписка: оплата счёта и постороннее поступление; конечный остаток больше журнала на 1 500 ₽.
	file := testStatement(t, 2_150_000, [3]string{"20000.00", "7707083893", "Оплата по счёту " + inv.Number},
		[3]string{"1500.00", "7707083893", "Оплата от ИНН 7707083893"})
	if _, err := run("statement", "import", "--file", file, "--email", "moderator@example.ru"); !errors.Is(err, errNotSenior) {
		t.Fatalf("import by moderator: %v", err)
	}
	out, err := run("statement", "import", "--file", file, "--email", "senior1@example.ru")
	if err != nil || !strings.Contains(out, "matched 1, unmatched 1, duplicates 0") {
		t.Fatalf("import: %q %v", out, err)
	}
	if _, err := run("statement", "import", "--file", file, "--email", "senior1@example.ru"); !errors.Is(err, billingdomain.ErrStatementImported) {
		t.Fatalf("import twice: %v", err)
	}
	out, err = run("bank-tx", "list", "--status", "unmatched")
	if err != nil || strings.Contains(out, "7707083893") || !strings.Contains(out, "77****3893") || !strings.Contains(out, "1 bank transaction(s)") {
		t.Fatalf("list: %q %v", out, err)
	}
	out, err = run("reconcile")
	if err != nil || !strings.Contains(out, "closing 21500.00 RUB, ledger bank 20000.00 RUB, difference 1500.00 RUB") ||
		!strings.Contains(out, "unexplained difference (minus unmatched): 0.00 RUB") || !strings.Contains(out, "unmatched incoming: 1, 1500.00 RUB") {
		t.Fatalf("reconcile: %q %v", out, err)
	}
	page, err := a.mods.billing.ListBankTransactions(ctx, billingdomain.MatchUnmatched, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("unmatched: %+v %v", page, err)
	}
	if _, err := run("bank-tx", "match", "--id", page.Items[0].ID.String(), "--org", inn, "--reason", "письмо клиента",
		"--email", "senior1@example.ru"); err != nil {
		t.Fatal(err)
	}
	available, _, err := a.mods.ledger.Balance(ctx, org)
	if err != nil || available != 2_150_000 {
		t.Fatalf("balance after match %d %v", available, err)
	}

	// Ручная проводка выше порога 100 000 ₽: без второго senior и с тем же — отказ.
	manual := []string{"ledger", "manual", "--debit", "bank", "--credit", "advertiser_balance:" + inn, "--amount", "100000.01",
		"--reason", "корректировка", "--email", "senior1@example.ru", "--key", "manual-1"}
	if _, err := run(manual...); !errors.Is(err, ledgerapp.ErrSecondApproval) {
		t.Fatalf("manual without approver: %v", err)
	}
	if _, err := run(append(manual, "--approver-email", "senior1@example.ru")...); !errors.Is(err, ledgerapp.ErrSecondApproval) {
		t.Fatalf("manual approved by self: %v", err)
	}
	if _, err := run(append(manual, "--approver-email", "moderator@example.ru")...); !errors.Is(err, errNotSenior) {
		t.Fatalf("manual approved by moderator: %v", err)
	}
	for range 2 {
		if _, err := run(append(manual, "--approver-email", "senior2@example.ru")...); err != nil {
			t.Fatal(err)
		}
	}
	if n := a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'manual'`); n != 1 {
		t.Fatalf("manual transactions %d", n)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'ledger.manual' AND actor_type = 'admin'
		AND actor_user_id IS NOT NULL AND data->>'approver_id' IS NOT NULL`); n != 1 {
		t.Fatalf("manual audit %d", n)
	}

	// Возврат остатка: больше остатка — отказ; повтор ключа — одна проводка.
	_, err = run("balance-return", "--org", org.String(), "--amount", "200000.00", "--reason", "возврат", "--email", "senior1@example.ru")
	var neg *ledgerdomain.NegativeLiabilityError
	if !errors.As(err, &neg) {
		t.Fatalf("return above balance: %v", err)
	}
	ret := []string{"balance-return", "--org", inn, "--amount", "1000.00", "--reason", "возврат по заявлению",
		"--email", "senior1@example.ru", "--key", "return-1"}
	for range 2 {
		if _, err := run(ret...); err != nil {
			t.Fatal(err)
		}
	}
	if n := a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'balance_return'`); n != 1 {
		t.Fatalf("balance_return transactions %d", n)
	}
	available, _, _ = a.mods.ledger.Balance(ctx, org)
	if available != 2_150_000+10_000_001-100_000 {
		t.Fatalf("balance %d", available)
	}

	// Отмена счёта по номеру.
	var second invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 300_000}).json(t, &second)
	if _, err := run("invoice", "cancel", "--number", second.Number, "--reason", "дубль", "--email", "senior1@example.ru"); err != nil {
		t.Fatal(err)
	}
	if n := a.count(`SELECT count(*) FROM invoice WHERE status = 'cancelled'`); n != 1 {
		t.Fatal("invoice is not cancelled")
	}

	out, err = run("ledger", "list", "--org", inn, "--since", "2020-01-01")
	if err != nil || !strings.Contains(out, "manual") || !strings.Contains(out, "balance_return") || !strings.Contains(out, "deposit") ||
		strings.Contains(out, inn) {
		t.Fatalf("ledger list: %q %v", out, err)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE actor_type = 'admin' AND action IN
		('bank_statement.imported', 'bank_transaction.matched', 'ledger.balance_return', 'invoice.cancelled')`); n != 4 {
		t.Fatalf("admin audit entries %d", n)
	}

	// Ручной запуск закрывающих документов: распределений нет — актов нет.
	if out, err := run("closing-acts", "--period", "2026-10", "--email", "senior1@example.ru"); err != nil ||
		!strings.Contains(out, "closing acts 2026-10: created 0, skipped 0, failed 0") {
		t.Fatalf("closing-acts: %q %v", out, err)
	}

	for _, args := range [][]string{{}, {"bogus"}, {"ledger"}, {"ledger", "manual", "--debit", "bank"},
		{"closing-acts", "--period", "10.2026", "--email", "senior1@example.ru"},
		{"ledger", "manual", "--debit", "bogus", "--credit", "bank", "--amount", "1.00", "--reason", "r", "--email", "senior1@example.ru"},
		{"balance-return", "--org", inn, "--amount", "-1", "--reason", "r", "--email", "senior1@example.ru"},
		{"bank-tx", "ignore", "--id", "x", "--reason", "r", "--email", "senior1@example.ru"}} {
		if _, err := run(args...); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}
