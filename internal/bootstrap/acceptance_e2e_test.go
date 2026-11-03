package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// Этап «Приёмка, споры и возвраты»: P-12, P-13, P-14 рекламодателем, P-15 командой администратора.

// publishedHTTP — размещение опубликовано (план 2026-10-20 09:00 UTC, снятие 2026-10-21 09:00).
func (a *testApp) publishedHTTP(adv, blg *client, title string) (orderID, pid string) {
	a.t.Helper()
	orderID, pid = a.scheduledHTTP(adv, blg, title)
	a.clk.Set(time.Date(2026, 10, 20, 9, 0, 3, 0, time.UTC))
	a.settle()
	if st, _ := a.placementState(pid); st != "published" {
		a.t.Fatalf("not published: %s", st)
	}
	return orderID, pid
}

// removedHTTP — то же, снятое по сроку: окно претензий до 2026-10-22 09:00.
func (a *testApp) removedHTTP(adv, blg *client, title string) (orderID, pid string) {
	a.t.Helper()
	orderID, pid = a.publishedHTTP(adv, blg, title)
	a.clk.Set(time.Date(2026, 10, 21, 9, 0, 5, 0, time.UTC))
	a.settle()
	if st, _ := a.placementState(pid); st != "removed" {
		a.t.Fatalf("not removed: %s", st)
	}
	return orderID, pid
}

func (a *testApp) ledgerOK(what string) {
	a.t.Helper()
	if rep, err := a.mods.ledger.CheckBalance(context.Background()); err != nil || !rep.OK() {
		a.t.Fatalf("%s: ledger check %+v: %v", what, rep, err)
	}
}

func (a *testApp) available(adv *client) int64 {
	a.t.Helper()
	var bal struct {
		Available int64 `json:"available_amount"`
	}
	adv.do(http.MethodGet, "/balance", nil).json(a.t, &bal)
	return bal.Available
}

// disputeAdmin — maxim admin dispute … от имени модератора.
func (a *testApp) disputeAdmin(args ...string) (string, error) {
	a.t.Helper()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	err := runAdmin(context.Background(), cfg, a.mods.in.log, append([]string{"dispute"}, args...), &out)
	return out.String(), err
}

type acceptView struct {
	Status           string   `json:"status"`
	PerformanceShare *int     `json:"performance_share"`
	RefundAmount     *int64   `json:"refund_amount"`
	AvailableActions []string `json:"available_actions"`
	Claim            *struct {
		Reason string `json:"reason"`
	} `json:"claim"`
	Dispute *struct {
		OpenedByType string `json:"opened_by_type"`
		Resolution   string `json:"resolution"`
	} `json:"dispute"`
}

func TestAcceptanceByAdvertiserAndAuto(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-accept@example.ru")
	blg := a.bloggerClient("blg-accept@example.ru")
	_, pid := a.publishedHTTP(adv, blg, "Приёмка")
	var v acceptView

	// Из published приёмка недопустима.
	r := adv.do(http.MethodPost, "/placements/"+pid+"/accept", nil)
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "invalid_transition" {
		t.Fatalf("accept published: %d %s", r.status, r.body)
	}
	a.clk.Set(time.Date(2026, 10, 21, 9, 0, 5, 0, time.UTC))
	a.settle()

	// S-08 P-12 с ключом; повтор — то же размещение, вторая выплата не создаётся.
	r = adv.doKey(http.MethodPost, "/placements/"+pid+"/accept", "accept-1", nil)
	if r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	r.json(t, &v)
	if v.Status != "accepted" || v.PerformanceShare == nil || *v.PerformanceShare != 10000 || len(v.AvailableActions) != 0 {
		t.Fatalf("accepted %+v", v)
	}
	if r := adv.doKey(http.MethodPost, "/placements/"+pid+"/accept", "accept-1", nil); r.status != http.StatusOK {
		t.Fatalf("repeat: %d %s", r.status, r.body)
	}
	if n := a.count(`SELECT count(*) FROM payout WHERE placement_id = $1 AND status IN ('created', 'sent')`, pid); n != 1 {
		t.Fatalf("payouts %d", n)
	}
	if n := a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'settlement' AND placement_id = $1`, pid); n != 1 {
		t.Fatalf("settlement %d", n)
	}
	a.ledgerOK("P-12")

	// S-24 P-13: второе размещение после окна претензий принимается системой.
	b := newTestApp(t)
	adv2 := b.advertiserClient("adv-auto@example.ru")
	blg2 := b.bloggerClient("blg-auto@example.ru")
	_, pid2 := b.removedHTTP(adv2, blg2, "Автоприёмка")
	b.clk.Set(time.Date(2026, 10, 22, 9, 0, 10, 0, time.UTC))
	b.settle()
	if st, reason := b.placementState(pid2); st != "accepted" || reason != "auto_acceptance" {
		t.Fatalf("auto accept: %s %s", st, reason)
	}
	if n := b.count(`SELECT count(*) FROM payout WHERE placement_id = $1`, pid2); n != 1 {
		t.Fatalf("auto payout %d", n)
	}
	b.ledgerOK("P-13")
}

func TestClaimAndProportionalResolution(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-claim@example.ru")
	blg := a.bloggerClient("blg-claim@example.ru")
	orderID, pid := a.removedHTTP(adv, blg, "Претензия")
	if _, err := a.mods.account.SetAdminRole(context.Background(), "moderator-d@example.ru",
		ptr(accountdomain.AdminModerator)); err != nil {
		t.Fatal(err)
	}

	// Пустой текст — 422; претензия — 201 и disputed.
	if r := adv.do(http.MethodPost, "/placements/"+pid+"/claims", map[string]any{"reason": "other", "text": ""}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("empty text: %d %s", r.status, r.body)
	}
	r := adv.do(http.MethodPost, "/placements/"+pid+"/claims", map[string]any{"reason": "content_mismatch", "text": "Не тот текст"})
	if r.status != http.StatusCreated {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	var v acceptView
	r.json(t, &v)
	if v.Status != "disputed" || v.Claim == nil || v.Dispute == nil || v.Dispute.OpenedByType != "advertiser" {
		t.Fatalf("claimed %+v", v)
	}
	r = adv.do(http.MethodPost, "/placements/"+pid+"/claims", map[string]any{"reason": "other", "text": "ещё"})
	if code, d := r.errorCode(t); r.status != http.StatusConflict || code != "conflict" || d["reason"] != "claim_exists" {
		t.Fatalf("second claim: %d %s", r.status, r.body)
	}

	// Команды администратора: не администратор — отказ; список; решение proportional 7500 (03 §7).
	if _, err := a.disputeAdmin("resolve", "--placement", pid, "--resolution", "full_payout", "--comment", "x",
		"--email", "adv-claim@example.ru"); !errors.Is(err, errAdminRequired) {
		t.Fatalf("not admin: %v", err)
	}
	out, err := a.disputeAdmin("list")
	if err != nil || !strings.Contains(out, pid) || !strings.Contains(out, "content_mismatch") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, err := a.disputeAdmin("show", "--placement", pid); err != nil || !strings.Contains(out, "Не тот текст") {
		t.Fatalf("show: %q %v", out, err)
	}
	// Команда идёт по системным часам — позже срока T-06 (2026-10-24 09:00 UTC по часам теста).
	if out, err := a.disputeAdmin("list", "--overdue"); err != nil || !strings.Contains(out, pid) {
		t.Fatalf("list --overdue: %q %v", out, err)
	}
	if _, err := a.disputeAdmin("resolve", "--placement", pid, "--resolution", "proportional", "--comment", "частично",
		"--email", "moderator-d@example.ru"); err == nil {
		t.Fatal("proportional without share and suggestion")
	}
	before := a.available(adv)
	out, err = a.disputeAdmin("resolve", "--placement", pid, "--resolution", "proportional", "--share", "7500",
		"--comment", "пост соответствовал частично", "--email", "moderator-d@example.ru")
	if err != nil || !strings.Contains(out, "accepted") || !strings.Contains(out, "payout 7500.00 RUB") ||
		!strings.Contains(out, "refund 2875.00 RUB") {
		t.Fatalf("resolve: %q %v", out, err)
	}
	if got := a.available(adv) - before; got != 287_500 {
		t.Fatalf("refund to balance %d", got)
	}
	if n := a.count(`SELECT count(*) FROM payout WHERE placement_id = $1 AND amount = 750000`, pid); n != 1 {
		t.Fatalf("payout %d", n)
	}
	if n := a.count(`SELECT count(*) FROM notification WHERE template = 'placement.dispute_resolved' AND channel = 'cabinet'`); n != 2 {
		t.Fatalf("dispute_resolved notices %d", n)
	}
	var ov struct {
		Placements []acceptView `json:"placements"`
	}
	adv.do(http.MethodGet, "/orders/"+orderID, nil).json(t, &ov)
	v = ov.Placements[0]
	if v.Dispute == nil || v.Dispute.Resolution != "proportional" || v.RefundAmount == nil || *v.RefundAmount != 287_500 {
		t.Fatalf("view after resolve %+v", v)
	}
	if _, err := a.disputeAdmin("resolve", "--placement", pid, "--resolution", "full_refund", "--comment", "передумали",
		"--email", "moderator-d@example.ru"); err == nil {
		t.Fatal("second resolution")
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action = 'dispute.resolved' AND actor_type = 'admin'`); n != 1 {
		t.Fatalf("audit %d", n)
	}
	a.ledgerOK("P-15 proportional")
}

func TestClaimFullRefundAndWindow(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-refund@example.ru")
	blg := a.bloggerClient("blg-refund@example.ru")
	orderID, pid := a.publishedHTTP(adv, blg, "Возврат")
	if _, err := a.mods.account.SetAdminRole(context.Background(), "senior-d@example.ru", ptr(accountdomain.AdminSenior)); err != nil {
		t.Fatal(err)
	}
	var rating int
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT c.rating FROM channel c JOIN placement p ON p.channel_id = c.id
		WHERE p.id = $1`, pid).Scan(&rating); err != nil {
		t.Fatal(err)
	}
	if r := adv.do(http.MethodPost, "/placements/"+pid+"/claims", map[string]any{"reason": "top_violated", "text": "Чужой пост сверху"}); r.status != http.StatusCreated {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	before := a.available(adv)
	out, err := a.disputeAdmin("resolve", "--placement", pid, "--resolution", "full_refund", "--comment", "топ нарушен",
		"--email", "senior-d@example.ru")
	if err != nil || !strings.Contains(out, "refunded") || !strings.Contains(out, "post removal queued") {
		t.Fatalf("resolve: %q %v", out, err)
	}
	// Команда идёт по системным часам: задание снятия поставлено на их «сейчас».
	a.clk.Set(time.Now().Add(time.Minute))
	a.settle()
	if st, _ := a.placementState(pid); st != "refunded" {
		t.Fatalf("status %s", st)
	}
	if got := a.available(adv) - before; got != 1_150_000 {
		t.Fatalf("refund %d", got)
	}
	posts := a.maxFake().Posts()
	if len(posts) != 1 || !slices.Contains(a.maxFake().Deleted(), posts[0].ID) {
		t.Fatal("post not deleted")
	}
	if n := a.count(`SELECT count(*) FROM publication WHERE placement_id = $1 AND removal_reason = 'refund'`, pid); n != 1 {
		t.Fatal("publication not removed with refund")
	}
	var after int
	_ = a.db.Pool.QueryRow(context.Background(), `SELECT c.rating FROM channel c JOIN placement p ON p.channel_id = c.id
		WHERE p.id = $1`, pid).Scan(&after)
	if after != max(rating-10, 0) {
		t.Fatalf("rating %d → %d", rating, after)
	}
	var o struct {
		Status string `json:"status"`
	}
	adv.do(http.MethodGet, "/orders/"+orderID, nil).json(t, &o)
	if o.Status != "completed" {
		t.Fatalf("order %s", o.Status)
	}
	a.ledgerOK("P-15 full refund")

	// Окно претензий закрыто — 409 claim_window_closed.
	b := newTestApp(t)
	adv2 := b.advertiserClient("adv-late@example.ru")
	blg2 := b.bloggerClient("blg-late@example.ru")
	_, pid2 := b.removedHTTP(adv2, blg2, "Поздно")
	b.clk.Set(time.Date(2026, 10, 22, 9, 0, 5, 0, time.UTC))
	r := adv2.do(http.MethodPost, "/placements/"+pid2+"/claims", map[string]any{"reason": "other", "text": "поздно"})
	if code, d := r.errorCode(t); r.status != http.StatusConflict || code != "invalid_transition" || d["reason"] != "claim_window_closed" {
		t.Fatalf("late claim: %d %s", r.status, r.body)
	}
}

func ptr[T any](v T) *T { return &v }
