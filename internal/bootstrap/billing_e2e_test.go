package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
)

// e2eUnmatchedPurpose — назначение постороннего платежа: в журнале его быть не должно.
const e2eUnmatchedPurpose = "Оплата по договору поставки 77 от ООО Ромашка"

// Этап «Приём оплаты и учёт денег» целиком (S-06, S-30, S-19, R-1 … R-17) на собранном api и
// worker: счёт, выписка, удержания, распределение, закрывающие документы, сверка.
func TestBillingEndToEnd(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	cli := func(mode string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		var err error
		if mode == "dev" {
			err = runDev(ctx, cfg, a.mods.in.log, args, &out)
		} else {
			err = runAdmin(ctx, cfg, a.mods.in.log, append([]string{"money"}, args...), &out)
		}
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", mode, args, err, out.String())
		}
		return out.String()
	}
	writeFile := func(name string, data []byte) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	role := accountdomain.AdminSenior
	if _, err := a.mods.account.SetAdminRole(ctx, "senior.money@example.ru", &role); err != nil {
		t.Fatal(err)
	}
	type balanceView struct {
		Available int64 `json:"available_amount"`
		Held      int64 `json:"held_amount"`
	}

	// S-06: реквизиты с банком, счёт на 20 000 ₽ и его PDF.
	adv := a.newAdvertiser("adv.money.e2e@example.ru")
	org := adv.orgID(t)
	var inn, orgName string
	if err := a.db.Pool.QueryRow(ctx, `SELECT inn, name FROM organization WHERE id = $1`, org).Scan(&inn, &orgName); err != nil {
		t.Fatal(err)
	}
	if r := adv.do(http.MethodPatch, "/organizations/current", map[string]any{"bank_account": "40702810938000000005",
		"bank_name": "ПАО СБЕРБАНК", "bank_bik": "044525225", "bank_corr_account": "30101810400000000225"}); r.status != http.StatusOK {
		t.Fatalf("bank details: %d %s", r.status, r.body)
	}
	var inv invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 2_000_000}).json(t, &inv)
	if r := adv.do(http.MethodGet, "/invoices/"+inv.ID+"/pdf", nil); r.status != http.StatusOK || !bytes.HasPrefix(r.body, []byte("%PDF")) {
		t.Fatalf("invoice pdf: %d", r.status)
	}
	blg := a.bloggerClient("blg.money.e2e@example.ru")
	ch := a.activeChannel(blg, "Деньги", 1_000_000)
	campaign := a.campaignWithPost(adv, "Деньги")
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("cart: %d %s", r.status, r.body)
	}
	r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil)
	if code, details := r.errorCode(t); r.status != http.StatusConflict || code != "insufficient_balance" || details["missing_amount"] != float64(1_150_000) {
		t.Fatalf("checkout without money: %d %s", r.status, r.body)
	}

	// S-30: выписка с оплатой счёта; тот же файл — отказ; другой файл с той же операцией — повтор.
	statement := []byte(cli("dev", "statement", "--invoice", inv.Number))
	out := cli("admin", "statement", "import", "--file", writeFile("s1.txt", statement), "--email", "senior.money@example.ru")
	if !strings.Contains(out, "matched 1, unmatched 0, duplicates 0, credited 20000.00 RUB") {
		t.Fatalf("import: %s", out)
	}
	var invoices struct{ Items []invoiceView }
	adv.do(http.MethodGet, "/invoices", nil).json(t, &invoices)
	if invoices.Items[0].Status != "paid" {
		t.Fatalf("invoice %+v", invoices.Items[0])
	}
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"money", "statement", "import", "--file", writeFile("s1-again.txt", statement),
		"--email", "senior.money@example.ru"}, &bytes.Buffer{}); err == nil {
		t.Fatal("same statement imported twice")
	}
	copyFile := bytes.Replace(statement, []byte("Отправитель=maxim dev"), []byte("Отправитель=maxim dev (копия)"), 1)
	out = cli("admin", "statement", "import", "--file", writeFile("s1-copy.txt", copyFile), "--email", "senior.money@example.ru")
	if !strings.Contains(out, "matched 0, unmatched 0, duplicates 1") {
		t.Fatalf("copy import: %s", out)
	}
	var bal balanceView
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 2_000_000 || bal.Held != 0 {
		t.Fatalf("balance after deposit %+v", bal)
	}

	// Оформление — удержание; отклонение версии — снято; новая версия — снова; отказ блогера — снято.
	var order orderView
	if r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil); r.status != http.StatusCreated {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	} else {
		r.json(t, &order)
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 850_000 || bal.Held != 1_150_000 {
		t.Fatalf("balance after checkout %+v", bal)
	}
	first := uuidOf(t, order.CurrentPostVersion.ID)
	v, _ := a.mods.post.Versions.Get(ctx, first)
	if _, err := a.mods.post.RejectVersion(ctx, first, v.CreatedBy, "Нет пометки «Реклама»"); err != nil {
		t.Fatal(err)
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 2_000_000 || bal.Held != 0 {
		t.Fatalf("balance after rejection %+v", bal)
	}
	a.savePost(adv, campaign, "Скидки, исправлено")
	var edited orderView
	if r := adv.do(http.MethodPost, "/orders/"+order.ID+"/post-versions", nil); r.status != http.StatusCreated {
		t.Fatalf("post version: %d %s", r.status, r.body)
	} else {
		r.json(t, &edited)
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 850_000 || bal.Held != 1_150_000 {
		t.Fatalf("balance after new version %+v", bal)
	}
	a.readyVersion(edited.CurrentPostVersion.ID)
	p := a.placementOf(order.ID, ch)
	if r := blg.do(http.MethodPost, "/blogger/placements/"+p+"/decline", map[string]any{"comment": "Не моя тематика"}); r.status != http.StatusOK {
		t.Fatalf("decline: %d %s", r.status, r.body)
	}
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 2_000_000 || bal.Held != 0 {
		t.Fatalf("balance after decline %+v", bal)
	}
	var ops operationsPage
	adv.do(http.MethodGet, "/balance/transactions?limit=100", nil).json(t, &ops)
	var kinds []string
	for _, op := range ops.Items {
		kinds = append(kinds, op.Kind)
	}
	// Служебные команды идут по системным часам, кабинет — по часам теста: порядок не сравнивается.
	slices.Sort(kinds)
	if strings.Join(kinds, ",") != "deposit,hold,hold,hold_release,hold_release" {
		t.Fatalf("operations %v", kinds)
	}

	// Распределение с долей 7500 (имитация приёмки) и закрывающие документы за октябрь.
	campaign2 := a.campaignWithPost(adv, "Деньги 2")
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("cart 2: %d %s", r.status, r.body)
	}
	var order2 orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign2+"/checkout", nil).json(t, &order2)
	p2 := uuidOf(t, a.placementOf(order2.ID, ch))
	if err := a.mods.txRunner.InTx(ctx, func(ctx context.Context) error {
		return a.mods.placement.Holds.Settle(ctx, p2, blg.orgID(t), placementdomain.Distribution{PerformanceShare: 7500,
			PayoutAmount: 750_000, FeeCharged: 112_500, RefundAmount: 287_500}, placementdomain.Settler{Actor: placementdomain.ActorSystem})
	}); err != nil {
		t.Fatal(err)
	}
	october := billingdomain.PeriodOf(a.clk.Now())
	if res, err := a.mods.billing.GenerateClosingActs(ctx, october); err != nil || res.Created != 1 {
		t.Fatalf("closing acts: %+v %v", res, err)
	}
	var acts struct {
		Items []struct {
			ID               string `json:"id"`
			PlacementsAmount int64  `json:"placements_amount"`
			FeeAmount        int64  `json:"fee_amount"`
			ReportURL        string `json:"report_url"`
			ActURL           string `json:"act_url"`
		} `json:"items"`
	}
	adv.do(http.MethodGet, "/closing-acts", nil).json(t, &acts)
	if len(acts.Items) != 1 || acts.Items[0].PlacementsAmount != 750_000 || acts.Items[0].FeeAmount != 112_500 {
		t.Fatalf("closing acts %+v", acts)
	}
	for _, url := range []string{acts.Items[0].ReportURL, acts.Items[0].ActURL} {
		if r := adv.do(http.MethodGet, strings.TrimPrefix(url, BaseURL), nil); r.status != http.StatusOK || !bytes.HasPrefix(r.body, []byte("%PDF")) {
			t.Fatalf("%s: %d", url, r.status)
		}
	}

	// Через 14 суток невыплаченный счёт истекает (worker); поступление по нему зачисляется.
	var late invoiceView
	adv.do(http.MethodPost, "/invoices", map[string]any{"amount": 500_000}).json(t, &late)
	if err := a.worker.EnsurePeriodic(ctx); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(14*24*time.Hour + time.Minute)
	a.settle()
	adv.do(http.MethodGet, "/invoices", nil).json(t, &invoices)
	if invoices.Items[0].ID != late.ID || invoices.Items[0].Status != "expired" {
		t.Fatalf("expired invoice %+v", invoices.Items[0])
	}
	s2 := filepath.Join(t.TempDir(), "s2.txt")
	if out := cli("dev", "statement", "--invoice", late.Number, "--out", s2); !strings.Contains(out, "written to") {
		t.Fatalf("dev statement --out: %s", out)
	}
	out = cli("admin", "statement", "import", "--file", s2, "--email", "senior.money@example.ru")
	if !strings.Contains(out, "matched 1") {
		t.Fatalf("expired invoice payment: %s", out)
	}
	adv.do(http.MethodGet, "/invoices", nil).json(t, &invoices)
	if invoices.Items[0].Status != "paid" {
		t.Fatalf("expired invoice after payment %+v", invoices.Items[0])
	}

	// Поступление без номера с чужим ИНН — unmatched и оповещение; сверка показывает только его.
	totals, err := a.mods.ledger.Totals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closing := totals[ledgerdomain.AccountBank] + 300_000
	unmatched := devStatementFile(devPlatformBankAccount, time.Now(), &closing,
		devPayment{number: "77", amount: 300_000, inn: "7707083893", purpose: e2eUnmatchedPurpose})
	out = cli("admin", "statement", "import", "--file", writeFile("s3.txt", unmatched), "--email", "senior.money@example.ru")
	if !strings.Contains(out, "matched 0, unmatched 1") {
		t.Fatalf("unmatched import: %s", out)
	}
	if !a.alertRaised("bank_transaction_unmatched") {
		t.Fatal("unmatched alert is missing")
	}
	out = cli("admin", "reconcile")
	if !strings.Contains(out, "difference 3000.00 RUB") || !strings.Contains(out, "unexplained difference (minus unmatched): 0.00 RUB") {
		t.Fatalf("reconcile: %s", out)
	}
	page, err := a.mods.billing.ListBankTransactions(ctx, billingdomain.MatchUnmatched, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("unmatched %+v %v", page, err)
	}
	cli("admin", "bank-tx", "match", "--id", page.Items[0].ID.String(), "--org", inn, "--reason", "Письмо плательщика",
		"--email", "senior.money@example.ru")
	adv.do(http.MethodGet, "/balance", nil).json(t, &bal)
	if bal.Available != 2_000_000-1_150_000+287_500+500_000+300_000 || bal.Held != 0 {
		t.Fatalf("final balance %+v", bal)
	}
	out = cli("admin", "reconcile")
	if !strings.Contains(out, "difference 0.00 RUB") || !strings.Contains(out, "unmatched incoming: 0, 0.00 RUB") {
		t.Fatalf("final reconcile: %s", out)
	}

	// Сходимость после всех шагов — без оповещений журнала.
	report, err := a.mods.ledger.CheckBalance(ctx)
	if err != nil || !report.OK() {
		t.Fatalf("ledger check %+v %v", report, err)
	}
	for _, reason := range []string{"ledger_imbalance", "ledger_cache_mismatch", "ledger_hold_mismatch", "ledger_negative_liability"} {
		if a.alertRaised(reason) {
			t.Errorf("alert %s raised", reason)
		}
	}
	a.checkBillingLog(t, []string{inn, orgName, e2eUnmatchedPurpose, "40702810500000000004", "40702810938000000005"})
}

// checkBillingLog — события денег (06 §11) и отсутствие персональных данных (07 §5.4): ИНН
// только маскированным, без наименований, назначений платежей и счетов плательщиков.
func (a *testApp) checkBillingLog(t *testing.T, secrets []string) {
	t.Helper()
	logs := a.logs.Bytes()
	messages := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(logs))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if m, ok := rec["msg"].(string); ok {
			messages[m] = true
		}
	}
	for _, m := range []string{
		"invoice issued", "invoice expired", "bank statement imported", "payment credited", "payment duplicate skipped",
		"payment unmatched", "payment matched manually", "holds placed", "hold released", "hold distributed",
		"closing act created", "closing acts generated", "ledger balance check passed", "bank statement rejected",
	} {
		if !messages[m] {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(string(logs), s) {
			t.Errorf("log contains personal data: %q", s)
		}
	}
}
