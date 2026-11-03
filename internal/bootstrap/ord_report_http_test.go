package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// Этап «API администратора и отчётность ОРД»: ежемесячная статистика и акты (R-19 … R-23, S-31).

type ordReportView struct {
	Period          string `json:"period"`
	State           string `json:"state"`
	StatisticsCount int    `json:"statistics_count"`
	InvoicesCount   int    `json:"invoices_count"`
	PendingCount    int    `json:"pending_count"`
	ErrorCount      int    `json:"error_count"`
	Impressions     int64  `json:"impressions"`
	Statistics      []struct {
		ID          string `json:"id"`
		PlacementID string `json:"placement_id"`
		Impressions int64  `json:"impressions"`
		ShownFrom   string `json:"shown_from"`
		ShownTo     string `json:"shown_to"`
		Status      string `json:"status"`
	} `json:"statistics"`
	Invoices []struct {
		ID           string `json:"id"`
		ContractKind string `json:"contract_kind"`
		Number       string `json:"number"`
		Amount       int64  `json:"amount"`
		Status       string `json:"status"`
		Organization struct {
			INN string `json:"inn"`
		} `json:"organization"`
	} `json:"invoices"`
}

// ordReportAdmin — maxim admin ord report … на часах системы.
func (a *testApp) ordReportAdmin(args ...string) (string, error) {
	a.t.Helper()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	err := runAdmin(context.Background(), cfg, a.mods.in.log, append([]string{"ord", "report"}, args...), &out)
	return out.String(), err
}

func TestOrdMonthlyReportEndToEnd(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	adv := a.advertiserClient("adv-report@example.ru")
	a.acceptOffers(adv)
	blg := a.bloggerClient("blg-report@example.ru")
	a.acceptOffers(blg)
	// Маркированный пост: креатив, площадка и договоры в ОРД (этап «Маркировка»).
	ch := a.moderatedChannel(blg, -73000000077, 5000)
	if r := a.adminClient("mod0.report@example.ru", accountdomain.AdminModerator).do(http.MethodPost, "/admin/channels/"+ch+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve channel: %d %s", r.status, r.body)
	}
	a.ordSettle()
	o := a.orderOn(adv, ch, "Отчётность")
	a.approveVersion(o.CurrentPostVersion.ID)
	a.ordSettle()
	for range 6 {
		a.clk.Advance(time.Minute)
		a.settle()
	}
	if n := a.count(`SELECT count(*) FROM creative WHERE sync_status <> 'synced'`); n != 0 {
		t.Fatalf("%d creatives not synced", n)
	}
	pid := o.Placements[0].ID
	if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	a.clk.Set(at(11, 30, 0))
	a.schedule(blg, pid, map[string]any{"mode": "now"})
	a.clk.Set(at(11, 30, 4))
	a.settle()
	a.clk.Set(at(11, 30, 4).Add(24*time.Hour + time.Minute))
	a.settle()
	if st, _ := a.placementState(pid); st != "removed" {
		t.Fatalf("not removed: %s", st)
	}
	a.ordSettle()

	mod := a.adminClient("mod.report@example.ru", accountdomain.AdminModerator)
	senior := a.adminClient("senior.report@example.ru", accountdomain.AdminSenior)

	// Октябрь не закрыт: формирование отклоняется, отчёта нет.
	r := senior.do(http.MethodPost, "/admin/ord/reports/2026-10/rebuild", map[string]any{"reason": "рано"})
	if code, _ := r.errorCode(t); r.status != http.StatusUnprocessableEntity || code != "validation_failed" {
		t.Fatalf("rebuild open month: %d %s", r.status, r.body)
	}
	var v ordReportView
	mod.do(http.MethodGet, "/admin/ord/reports/2026-10", nil).json(t, &v)
	if v.State != "not_built" {
		t.Fatalf("before build: %+v", v)
	}

	if err := a.worker.EnsurePeriodic(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1 ноября 00:05 МСК: снимок и формирование октября по расписанию, без отправки (ORD_REPORT_AUTO_SEND=false).
	a.clk.Set(time.Date(2026, 10, 31, 21, 6, 0, 0, time.UTC))
	a.settle()
	// Сессии администраторов — после движения часов.
	mod = a.adminClient("mod2.report@example.ru", accountdomain.AdminModerator)
	senior = a.adminClient("senior2.report@example.ru", accountdomain.AdminSenior)
	v = ordReportView{}
	mod.do(http.MethodGet, "/admin/ord/reports/2026-10", nil).json(t, &v)
	if v.State != "built" || v.StatisticsCount != 1 || v.InvoicesCount != 2 || len(v.Statistics) != 1 ||
		v.Statistics[0].PlacementID != pid || v.Statistics[0].ShownFrom != "2026-10-20" || v.Statistics[0].ShownTo != "2026-10-21" {
		t.Fatalf("built: %+v", v)
	}
	for _, inv := range v.Invoices {
		if inv.Amount <= 0 || !strings.HasPrefix(inv.Number, "MX-202610-") || strings.Count(inv.Organization.INN, "*") == 0 {
			t.Fatalf("invoice %+v", inv)
		}
	}
	if n := a.count(`SELECT count(*) FROM job WHERE kind = 'sync_ord_object' AND status = 'pending' AND payload->>'type' IN ('statistics', 'invoice')`); n != 0 {
		t.Fatalf("report sent without command: %d jobs", n)
	}

	// Модератор видит, но не формирует и не отправляет.
	for _, path := range []string{"/admin/ord/reports/2026-10/rebuild", "/admin/ord/reports/2026-10/send"} {
		if r := mod.do(http.MethodPost, path, map[string]any{"reason": "попытка"}); r.status != http.StatusForbidden {
			t.Fatalf("moderator %s: %d %s", path, r.status, r.body)
		}
	}
	// Основание обязательно.
	if r := senior.do(http.MethodPost, "/admin/ord/reports/2026-10/send", map[string]any{"reason": " "}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("send without reason: %d %s", r.status, r.body)
	}
	// Сентябрь не сформирован — отправлять нечего.
	r = senior.do(http.MethodPost, "/admin/ord/reports/2026-09/send", map[string]any{"reason": "проверка"})
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "conflict" {
		t.Fatalf("send not built: %d %s", r.status, r.body)
	}

	// Повторное формирование без изменений ничего не сбрасывает.
	v = ordReportView{}
	senior.do(http.MethodPost, "/admin/ord/reports/2026-10/rebuild", map[string]any{"reason": "сверка"}).json(t, &v)
	if v.State != "built" || v.StatisticsCount != 1 {
		t.Fatalf("rebuild: %+v", v)
	}

	// Отправка senior: статистика, акты; ordfake подтверждает.
	v = ordReportView{}
	senior.do(http.MethodPost, "/admin/ord/reports/2026-10/send", map[string]any{"reason": "ежемесячная отправка"}).json(t, &v)
	if v.State != "sending" {
		t.Fatalf("send: %+v", v)
	}
	a.ordSettle()
	a.ordSettle()
	v = ordReportView{}
	mod.do(http.MethodGet, "/admin/ord/reports/2026-10", nil).json(t, &v)
	if v.State != "sent" || v.PendingCount != 0 || v.ErrorCount != 0 {
		t.Fatalf("sent: %+v", v)
	}
	if _, ok := a.ord.Object(v.Statistics[0].ID); !ok {
		t.Fatal("statistics not in ordfake")
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action IN ('ord_report.built', 'ord_report.send_requested') AND actor_type = 'admin'`); n != 2 {
		t.Fatalf("audit %d", n)
	}

	// Список и консоль: show любой роли, send — только senior.
	var list struct {
		Items []ordReportView `json:"items"`
	}
	mod.do(http.MethodGet, "/admin/ord/reports", nil).json(t, &list)
	if len(list.Items) == 0 || list.Items[0].Period != "2026-10" || list.Items[0].State != "sent" {
		t.Fatalf("list: %+v", list)
	}
	out, err := a.ordReportAdmin("show", "--period", "2026-10")
	if err != nil || !strings.Contains(out, "period 2026-10: sent") {
		t.Fatalf("show: %v %s", err, out)
	}
	if _, err := a.ordReportAdmin("send", "--period", "2026-10", "--reason", "x", "--email", "mod2.report@example.ru"); !errors.Is(err, errNotSenior) {
		t.Fatalf("moderator send from console: %v", err)
	}
	if _, err := a.ordReportAdmin("build", "--period", "2026-10"); !errors.Is(err, errUsage) {
		t.Fatalf("build without reason: %v", err)
	}
	for _, secret := range []string{"adv-report@example.ru", "blg-report@example.ru"} {
		if bytes.Contains(a.logs.Bytes(), []byte(secret)) {
			t.Fatalf("log contains %s", secret)
		}
	}
}
