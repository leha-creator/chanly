package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/stuck"
)

// Цена размещения сквозного теста выплат, коп.
const payoutE2EPrice = 50_000

// payoutOrder — заказ рекламодателя из новых каналов блогера; версия поста готова, размещения
// ждут ответа блогера (принятие — в сценарии, после онбординга в Рокет Ворк).
func (a *testApp) payoutOrder(adv, blg *client, titles ...string) []string {
	a.t.Helper()
	campaign := a.campaignWithPost(adv, "Выплаты "+titles[0])
	a.savePost(adv, campaign, autopostText)
	var channels []string
	for _, title := range titles {
		ch := a.activeChannel(blg, title, payoutE2EPrice)
		if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
			a.t.Fatalf("add to cart: %d %s", r.status, r.body)
		}
		channels = append(channels, ch)
	}
	var order orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil).json(a.t, &order)
	a.readyVersion(order.CurrentPostVersion.ID)
	pids := make([]string, 0, len(channels))
	for _, ch := range channels {
		pids = append(pids, a.placementOf(order.ID, ch))
	}
	return pids
}

func (a *testApp) acceptPlacements(blg *client, pids ...string) {
	a.t.Helper()
	for _, pid := range pids {
		if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
			a.t.Fatalf("accept %s: %d %s", pid, r.status, r.body)
		}
	}
}

// payoutState — состояние, способ, попытка и код отказа последней выплаты размещения.
type payoutState struct {
	ID          string
	Status      string
	Method      string
	Attempt     int
	FailureCode string
	ExternalID  string
}

func (a *testApp) latestPayout(pid string) payoutState {
	a.t.Helper()
	var p payoutState
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT id::text, status, method, attempt, coalesce(failure_code, ''),
		coalesce(external_id, '') FROM payout WHERE placement_id = $1 ORDER BY attempt DESC LIMIT 1`, pid).
		Scan(&p.ID, &p.Status, &p.Method, &p.Attempt, &p.FailureCode, &p.ExternalID); err != nil {
		a.t.Fatalf("payout of %s: %v", pid, err)
	}
	return p
}

// Этап выплат, задача 13: S-11 → S-15 на собранных api и worker с подделкой Рокет Ворк,
// командами maxim dev и maxim admin, вебхуками и опросом.
func TestPayoutEndToEnd(t *testing.T) {
	cfg := testConfig()
	cfg.RocketWork.FakeStatePath = filepath.Join(t.TempDir(), "rocketwork-fake.json")
	a := newTestAppWith(t, cfg)
	ctx := withCommandClock(context.Background(), a.clk)
	cmdCfg := cfg
	cmdCfg.DatabaseURL = a.db.AppURL
	cmd := func(run func(context.Context, Config, *slog.Logger, []string, io.Writer) error, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(ctx, cmdCfg, a.mods.in.log, args, &out); err != nil {
			t.Fatalf("%v: %v (%s)", args, err, out.String())
		}
		return out.String()
	}
	dev := func(args ...string) string { return cmd(runDev, args...) }
	admin := func(args ...string) string { return cmd(runAdmin, args...) }
	for _, email := range []string{"senior.payout@example.ru", "senior2.payout@example.ru"} {
		role := accountdomain.AdminSenior
		if _, err := a.mods.account.SetAdminRole(ctx, email, &role); err != nil {
			t.Fatal(err)
		}
	}
	webhookURL := a.srv.URL + "/api/v1/webhooks/rocketwork"
	rw := a.rocketWork()

	// Депозит: проводка в журнале и остаток в сервисе совпадают.
	admin("money", "deposit-topup", "--amount", "5000.00", "--reason", "пополнение депозита", "--email", "senior.payout@example.ru",
		"--key", "rw-e2e-1")
	dev("rocketwork", "deposit", "--amount", "5000.00")

	// S-10 → S-11: самозанятый, приглашение, задание ensure_contractor — ссылка; без active принять нельзя.
	adv := a.advertiserClient("adv.payout@example.ru")
	blg, blgOrg := a.newBlogger("blg.payout@example.ru")
	var blgView struct {
		INN string `json:"inn"`
	}
	blg.do(http.MethodGet, "/organizations/current", nil).json(t, &blgView)
	var st rocketworkView
	blg.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Status != "none" || !st.Required {
		t.Fatalf("before invite %+v", st)
	}
	dev("rocketwork", "invite", "--org", blgOrg.String())
	dev("rocketwork", "invite", "--org", blgOrg.String()) // idempotent; normal cabinet remains usable
	if r := blg.do(http.MethodPost, "/blogger/rocketwork/invite", nil); r.status != http.StatusAccepted {
		t.Fatalf("invite: %d %s", r.status, r.body)
	}
	a.settle()
	blg.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Status == "active" || st.OnboardingURL == "" {
		t.Fatalf("after ensure %+v", st)
	}
	onboarding := st.OnboardingURL
	pids := a.payoutOrder(adv, blg, "Кухня", "Сад", "Дом", "Лес", "Море")
	r := blg.do(http.MethodPost, "/blogger/placements/"+pids[0]+"/accept", nil)
	if code, details := r.errorCode(t); r.status != http.StatusConflict || code != "conflict" || details["reason"] != "contractor_inactive" {
		t.Fatalf("accept without contractor: %d %s", r.status, r.body)
	}
	c, err := a.mods.payoutStore.ContractorByOrg(ctx, blgOrg, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.ActivateContractor(c.ExternalID); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(5 * time.Minute)
	a.settle()
	blg.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Status != "active" {
		t.Fatalf("after poll %+v", st)
	}
	a.acceptPlacements(blg, pids...)

	// Юрлицо: гейт Рокет Ворк не нужен (A-36).
	legal := a.legalBloggerClient("blg.payout.legal@example.ru")
	legalPids := a.payoutOrder(adv, legal, "Юрканал")
	a.acceptPlacements(legal, legalPids...)

	// Публикация и снятие по сроку: все размещения в removed.
	all := append(append([]string{}, pids...), legalPids...)
	a.clk.Set(at(11, 30, 0))
	for i, pid := range all {
		if i < len(pids) {
			a.schedule(blg, pid, map[string]any{"mode": "now"})
		} else {
			a.schedule(legal, pid, map[string]any{"mode": "now"})
		}
	}
	a.clk.Set(at(11, 30, 5))
	a.settle()
	a.clk.Set(at(11, 30, 5).Add(24*time.Hour + 10*time.Second))
	a.settle()
	for _, pid := range all {
		if s, _ := a.placementState(pid); s != "removed" {
			t.Fatalf("placement %s: %s", pid, s)
		}
	}

	// S-15: приёмка командой разработчика → выплата created → send_payout → sent → вебхук → succeeded, paid.
	out := dev("placement", "accept", "--placement", pids[0], "--share", "10000")
	if !strings.Contains(out, "accepted, payout ") {
		t.Fatalf("dev accept: %q", out)
	}
	if p := a.latestPayout(pids[0]); p.Status != "created" || p.Method != "rocketwork" {
		t.Fatalf("created %+v", p)
	}
	a.settle()
	p := a.latestPayout(pids[0])
	if p.Status != "sent" || p.ExternalID == "" {
		t.Fatalf("sent %+v", p)
	}
	if out := dev("rocketwork", "complete", "--payout", p.ID, "--webhook", webhookURL); !strings.Contains(out, "webhook") {
		t.Fatalf("dev complete: %q", out)
	}
	a.settle()
	if p := a.latestPayout(pids[0]); p.Status != "succeeded" {
		t.Fatalf("after webhook %+v", p)
	}
	if s, _ := a.placementState(pids[0]); s != "paid" {
		t.Fatalf("placement after payout: %s", s)
	}
	if n := a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'settlement' AND placement_id = $1`, pids[0]); n != 1 {
		t.Fatalf("settlement transactions %d", n)
	}
	if n := a.count(`SELECT count(*) FROM ledger_entry e JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
		WHERE t.kind = 'payout' AND t.payout_id = $1`, p.ID); n != 2 {
		t.Fatalf("payout entries (amount and service fee) %d", n)
	}
	var page payoutPage
	blg.do(http.MethodGet, "/blogger/payouts", nil).json(t, &page)
	var receipt string
	for _, it := range page.Items {
		if it.ID == p.ID {
			receipt = it.ReceiptURL
		}
	}
	if receipt == "" {
		t.Fatalf("receipt url is missing: %+v", page.Items)
	}

	// Опрос без вебхука: сервис завершил выплату, worker узнаёт об этом опросом.
	dev("placement", "accept", "--placement", pids[1])
	a.settle()
	p1 := a.latestPayout(pids[1])
	if p1.Status != "sent" {
		t.Fatalf("poll scenario sent %+v", p1)
	}
	if err := rw.CompletePayout(p1.ExternalID, -1); err != nil {
		t.Fatal(err)
	}
	for range 13 {
		a.clk.Advance(5 * time.Minute)
		a.settle()
	}
	if p := a.latestPayout(pids[1]); p.Status != "succeeded" {
		t.Fatalf("after poll %+v", p)
	}
	if s, _ := a.placementState(pids[1]); s != "paid" {
		t.Fatalf("placement after poll: %s", s)
	}

	// Нехватка депозита: выплата ждёт и даёт оповещение; после пополнения уходит через час.
	balance := rw.Deposit()
	dev("rocketwork", "deposit", "--amount", "0.00")
	dev("placement", "accept", "--placement", pids[2])
	a.settle()
	if p := a.latestPayout(pids[2]); p.Status != "created" {
		t.Fatalf("deposit short %+v", p)
	}
	if a.count(`SELECT count(*) FROM alert WHERE reason = $1 AND resolved_at IS NULL`, payoutapp.AlertDepositInsufficient) != 1 {
		t.Fatal("deposit insufficient alert")
	}
	if err := rw.SetDeposit(balance); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(time.Hour + time.Second)
	a.settle()
	p2 := a.latestPayout(pids[2])
	if p2.Status != "sent" {
		t.Fatalf("after topup %+v", p2)
	}
	if a.count(`SELECT count(*) FROM alert WHERE reason = $1 AND resolved_at IS NULL`, payoutapp.AlertDepositInsufficient) != 0 {
		t.Fatal("deposit insufficient alert is not resolved")
	}
	dev("rocketwork", "complete", "--payout", p2.ID)

	// Утрачен статус самозанятого: выплата failed, размещение accepted; повтор после восстановления.
	dev("rocketwork", "block", "--org", blgOrg.String(), "--reason", "tax_status_lost", "--webhook", webhookURL)
	a.settle()
	blg.do(http.MethodGet, "/blogger/rocketwork", nil).json(t, &st)
	if st.Status != "blocked" || st.BlockedReason != "tax_status_lost" {
		t.Fatalf("blocked %+v", st)
	}
	dev("placement", "accept", "--placement", pids[3])
	a.settle()
	if p := a.latestPayout(pids[3]); p.Status != "failed" || p.FailureCode != "tax_status_lost" {
		t.Fatalf("tax status lost %+v", p)
	}
	if s, _ := a.placementState(pids[3]); s != "accepted" {
		t.Fatalf("placement after failure: %s", s)
	}
	dev("rocketwork", "activate", "--org", blgOrg.String())
	failed := a.latestPayout(pids[3])
	if out := admin("payout", "retry", "--payout", failed.ID, "--reason", "статус восстановлен", "--email", "senior.payout@example.ru"); !strings.Contains(out, "attempt 2") {
		t.Fatalf("retry: %q", out)
	}
	a.settle()
	p3 := a.latestPayout(pids[3])
	if p3.Attempt != 2 || p3.Status != "sent" {
		t.Fatalf("retry sent %+v", p3)
	}
	dev("rocketwork", "complete", "--payout", p3.ID, "--webhook", webhookURL)
	a.settle()
	if s, _ := a.placementState(pids[3]); s != "paid" {
		t.Fatalf("placement after retry: %s", s)
	}

	// Юрлицо: выплата переводом, отметка администратора — paid, проводка из bank.
	dev("placement", "accept", "--placement", legalPids[0])
	a.settle()
	lp := a.latestPayout(legalPids[0])
	if lp.Method != "bank_transfer" || lp.Status != "created" {
		t.Fatalf("legal payout %+v", lp)
	}
	admin("payout", "mark-paid", "--payout", lp.ID, "--bank-ref", "п/п 15", "--reason", "оплачено", "--email", "senior.payout@example.ru")
	if s, _ := a.placementState(legalPids[0]); s != "paid" {
		t.Fatalf("legal placement: %s", s)
	}
	if n := a.count(`SELECT count(*) FROM ledger_entry e JOIN ledger_transaction t ON t.id = e.ledger_transaction_id
		JOIN ledger_account c ON c.id = e.credit_account_id WHERE t.payout_id = $1 AND c.kind = 'bank'`, lp.ID); n != 1 {
		t.Fatalf("legal payout from bank: %d", n)
	}

	// T-12: отказ сервиса, выплата не повторена — через 72 ч оповещение.
	dev("placement", "accept", "--placement", pids[4])
	a.settle()
	p4 := a.latestPayout(pids[4])
	dev("rocketwork", "fail", "--payout", p4.ID, "--reason", "rejected")
	if p := a.latestPayout(pids[4]); p.Status != "failed" {
		t.Fatalf("rejected %+v", p)
	}
	a.clk.Advance(73 * time.Hour)
	a.settle()
	checker := stuck.New(a.mods.in.alerter, a.clk, a.mods.in.log)
	for _, ch := range a.mods.payout.StuckChecks() {
		checker.Add(ch)
	}
	if err := checker.Run(ctx, job.Running{}); err != nil {
		t.Fatal(err)
	}
	if n := a.count(`SELECT count(*) FROM alert WHERE reason = 'stuck:' || $1 AND object_key LIKE '%' || $2`,
		payoutapp.StuckAcceptedUnpaid, pids[4]); n != 1 {
		t.Fatalf("T-12 alert %d", n)
	}
	if n := a.count(`SELECT count(*) FROM alert WHERE reason LIKE 'stuck:payout%' AND resolved_at IS NULL`); n != 1 {
		t.Fatalf("stuck payout alerts %d", n)
	}

	// Итоги блогера сходятся со строками выплат.
	var sum struct {
		Received   int64 `json:"received_amount"`
		InProgress int64 `json:"in_progress_amount"`
		Count      int   `json:"count"`
	}
	blg.do(http.MethodGet, "/blogger/payouts/summary", nil).json(t, &sum)
	if want := int64(a.count(`SELECT coalesce(sum(amount), 0)::int FROM payout WHERE blogger_organization_id = $1 AND status = 'succeeded'`,
		blgOrg)); sum.Received != want || sum.Received == 0 || sum.InProgress != 0 || sum.Count != 6 {
		t.Fatalf("summary %+v, want received %d", sum, want)
	}

	// Уведомления блогеру (R-15): все коды выплат и статуса исполнителя, ссылка на выплаты, у
	// выплат — заказ и канал, у прошедшей через Рокет Ворк — чек.
	if n := a.count(`SELECT count(DISTINCT template)::int FROM notification WHERE organization_id = $1 AND channel = 'cabinet'
		AND template IN ('payout.sent', 'payout.succeeded', 'payout.failed', 'rocketwork.active', 'rocketwork.blocked')
		AND payload->>'link' = '/blg/payouts'`, blgOrg); n != 5 {
		t.Fatalf("payout notice templates %d, want 5", n)
	}
	if n := a.count(`SELECT count(*)::int FROM notification WHERE organization_id = $1 AND channel = 'cabinet'
		AND template LIKE 'payout.%' AND (payload->>'order_number' IS NULL OR payload->>'channel_id' IS NULL)`, blgOrg); n != 0 {
		t.Fatalf("payout notices without order or channel: %d", n)
	}
	if n := a.count(`SELECT count(*)::int FROM notification WHERE organization_id = $1 AND template = 'payout.succeeded'
		AND payload->>'receipt_url' = $2`, blgOrg, receipt); n == 0 {
		t.Fatal("payout.succeeded without receipt_url")
	}
	if page := blg.notifications(t, "?limit=100"); !hasTemplate(page, "payout.succeeded") || !hasTemplate(page, "rocketwork.active") {
		t.Fatalf("cabinet notices %+v", page.Items)
	}

	// Сверки после всех шагов: журнал сходится, депозит покрывает обязательства и совпадает с сервисом.
	if rep, err := a.mods.ledger.CheckBalance(ctx); err != nil || !rep.OK() {
		t.Fatalf("ledger check %+v: %v", rep, err)
	}
	rep, err := a.mods.payout.CheckDeposit(ctx)
	if err != nil || rep.Shortage() != 0 || rep.Mismatch() {
		t.Fatalf("deposit check %+v: %v", rep, err)
	}
	if n := a.count(`SELECT count(*) FROM alert WHERE resolved_at IS NULL AND (reason LIKE 'rocketwork_deposit%' OR reason LIKE 'ledger%')`); n != 0 {
		t.Fatalf("open money alerts %d", n)
	}
	if out := admin("money", "reconcile"); !strings.Contains(out, "mismatch false") {
		t.Fatalf("reconcile: %q", out)
	}

	a.checkPayoutLog(t, []string{blgView.INN, "Анна Блогерова", "+79990001122", onboarding, receipt,
		string(cfg.RocketWorkWebhookSecret())})
}

// checkPayoutLog — события этапа выплат (06 §11, 07 §5.3) есть; персональных данных и секретов нет.
func (a *testApp) checkPayoutLog(t *testing.T, secrets []string) {
	t.Helper()
	logs := a.logs.String()
	for _, m := range []string{
		"rocketwork invite requested", "rocketwork contractor ensured", "rocketwork contractor status changed",
		"rocketwork event processed", "payout created", "payout sent", "payout succeeded", "payout failed",
		"rocketwork deposit insufficient", "rocketwork deposit checked", "admin payout retry", "admin payout marked paid",
		"admin deposit topup", "placement accepted by dev command",
	} {
		if !strings.Contains(logs, `"msg":"`+m+`"`) {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("log contains personal data or a secret: %q", s)
		}
	}
}

// R-4: команды maxim dev rocketwork — только с подделкой; неполные аргументы — подсказка.
func TestDevPayoutCommands(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()
	var out bytes.Buffer
	for _, args := range [][]string{
		{"rocketwork", "invite"}, {"rocketwork", "activate"}, {"rocketwork", "deposit"}, {"rocketwork", "complete"}, {"placement", "accept"},
		{"rocketwork", "unknown", "--org", "x"}, {"rocketwork"},
	} {
		if err := runDevPayout(ctx, cfg, slog.New(slog.DiscardHandler), args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	cfg.RocketWork.Mode = RocketWorkDisabled
	if err := runDevPayout(ctx, cfg, slog.New(slog.DiscardHandler), []string{"rocketwork", "deposit", "--amount", "1.00"}, &out); !errors.Is(err, errDevRocketWorkFake) {
		t.Fatalf("disabled: %v", err)
	}
}
