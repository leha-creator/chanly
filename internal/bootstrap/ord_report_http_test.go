package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/ord"
	markingdomain "github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
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
	// Explicit financial inputs: absence of VAT configuration must not imply no VAT.
	if _, err := a.db.Pool.Exec(context.Background(), `UPDATE organization SET vat_rate='none' WHERE vat_rate IS NULL`); err != nil {
		t.Fatal(err)
	}
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

	// Report fixture supplies a measured value explicitly; absent MAX metrics stay unknown.
	if _, err := a.db.Pool.Exec(context.Background(), "UPDATE placement SET final_views=1200 WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
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
	a.ord.FailNext(ord.OpInvoiceCreatives, http.StatusUnprocessableEntity, "invalid test allocation")
	v = ordReportView{}
	senior.do(http.MethodPost, "/admin/ord/reports/2026-10/send", map[string]any{"reason": "ежемесячная отправка"}).json(t, &v)
	if v.State != "sending" {
		t.Fatalf("send: %+v", v)
	}
	a.ordSettle()
	a.ordSettle()
	if n := a.count(`SELECT count(*) FROM ord_invoice WHERE sync_status <> 'synced'`); n != 0 {
		t.Fatalf("document not confirmed: %d", n)
	}
	if n := a.count(`SELECT count(*) FROM ord_invoice_creatives WHERE sync_status='error'`); n != 1 {
		t.Fatalf("expected one failed links object, got %d", n)
	}
	documentCalls := a.ord.Calls(ord.OpInvoice)
	if r := senior.do(http.MethodPost, "/admin/ord/reports/2026-10/send", map[string]any{"reason": "retry links only"}); r.status != http.StatusOK {
		t.Fatalf("retry links: %d %s", r.status, r.body)
	}
	a.ordSettle()
	if a.ord.Calls(ord.OpInvoice) != documentCalls {
		t.Fatal("retrying links repeated document POST")
	}
	if n := a.count(`SELECT count(*) FROM ord_invoice i JOIN ord_invoice_creatives l ON l.ord_invoice_id=i.id WHERE i.sync_request_id IS NULL OR l.sync_request_id IS NULL OR i.sync_request_id=l.sync_request_id`); n != 0 {
		t.Fatal("independent receipts not persisted")
	}
	v = ordReportView{}
	mod.do(http.MethodGet, "/admin/ord/reports/2026-10", nil).json(t, &v)
	if v.State != "sent" || v.PendingCount != 0 || v.ErrorCount != 0 {
		t.Fatalf("sent: %+v", v)
	}
	if _, ok := a.ord.Object(v.Statistics[0].ID); !ok {
		t.Fatal("statistics not in ordfake")
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action IN ('ord_report.built', 'ord_report.send_requested') AND actor_type = 'admin'`); n != 3 {
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

	t.Run("missing metrics are not reported as zero", func(t *testing.T) {
		ctx := context.Background()
		var final, views *int32
		if err := a.db.Pool.QueryRow(ctx, "SELECT pl.final_views,p.views FROM placement pl JOIN publication p ON p.placement_id=pl.id WHERE pl.id=$1", pid).Scan(&final, &views); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE placement SET final_views=NULL WHERE id=$1", pid); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE publication SET views=NULL WHERE placement_id=$1", pid); err != nil {
			t.Fatal(err)
		}
		result := senior.do(http.MethodPost, "/admin/ord/reports/2026-10/rebuild", map[string]any{"reason": "check unknown metrics"})
		if result.status != http.StatusUnprocessableEntity {
			t.Fatalf("unknown views accepted: %d %s", result.status, result.body)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE placement SET final_views=$2 WHERE id=$1", pid, final); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE publication SET views=$2 WHERE placement_id=$1", pid, views); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("known receipts resume polling without POST", func(t *testing.T) {
		ctx := context.Background()
		tables := []string{"ord_statistics", "ord_invoice", "ord_invoice_creatives"}
		before := a.ord.Calls(ord.OpStatistics) + a.ord.Calls(ord.OpInvoice) + a.ord.Calls(ord.OpInvoiceCreatives)
		for _, table := range tables {
			if _, err := a.db.Pool.Exec(ctx, "UPDATE "+table+" SET sync_status='error',sync_error='status_timeout',ord_status='ERIR sync success'"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.mods.marking.SendReport(ctx, markingdomain.Period{Year: 2026, Month: time.October}, nil, ""); err != nil {
			t.Fatal(err)
		}
		a.ordSettle()
		after := a.ord.Calls(ord.OpStatistics) + a.ord.Calls(ord.OpInvoice) + a.ord.Calls(ord.OpInvoiceCreatives)
		if before != after {
			t.Fatal("timeout replayed an accepted report")
		}
		for _, table := range tables {
			if a.count("SELECT count(*) FROM "+table+" WHERE sync_status<>'synced'") != 0 {
				t.Fatal("receipt did not finish: " + table)
			}
		}
	})
	t.Run("permanent cancellation retains every report checkpoint", func(t *testing.T) {
		ctx := context.Background()
		original := a.mods.marking.Ord
		defer func() { a.mods.marking.Ord = original }()
		a.mods.marking.Ord = canceledReportAPI{original}
		cases := []struct {
			table, key, id string
			kind           markingdomain.ObjectType
			op             string
		}{
			{"ord_invoice_creatives", "ord_invoice_id", v.Invoices[0].ID, markingdomain.TypeInvoiceCreatives, ord.OpInvoiceCreatives},
			{"ord_invoice", "id", v.Invoices[0].ID, markingdomain.TypeInvoice, ord.OpInvoice},
			{"ord_statistics", "id", v.Statistics[0].ID, markingdomain.TypeStatistics, ord.OpStatistics},
		}
		for _, tc := range cases {
			t.Run(string(tc.kind), func(t *testing.T) {
				var savedReceipt *string
				if err := a.db.Pool.QueryRow(ctx, "SELECT sync_request_id FROM "+tc.table+" WHERE "+tc.key+"=$1", tc.id).Scan(&savedReceipt); err != nil {
					t.Fatal(err)
				}
				if _, err := a.db.Pool.Exec(ctx, "UPDATE "+tc.table+" SET sync_status='pending' WHERE "+tc.key+"=$1", tc.id); err != nil {
					t.Fatal(err)
				}
				ref := markingdomain.ObjectRef{Type: tc.kind, ID: uuid.MustParse(tc.id)}
				before := a.ord.Calls(tc.op)
				if err := a.mods.marking.Sync(ctx, ref, 1); err != nil {
					t.Fatal(err)
				}
				if err := a.mods.marking.Sync(ctx, ref, 2); err != nil {
					t.Fatal(err)
				}
				var uncertain bool
				if err := a.db.Pool.QueryRow(ctx, "SELECT submission_uncertain FROM "+tc.table+" WHERE "+tc.key+"=$1", tc.id).Scan(&uncertain); err != nil {
					t.Fatal(err)
				}
				if !uncertain || a.ord.Calls(tc.op) != before+1 {
					t.Fatal("canceled accepted POST lost its checkpoint or replayed")
				}
				// Restore this fixture only, after verifying the production guard.
				if _, err := a.db.Pool.Exec(ctx, "UPDATE "+tc.table+" SET submission_uncertain=false,sync_status='synced',sync_request_id=$2 WHERE "+tc.key+"=$1", tc.id, savedReceipt); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	t.Run("unknown outcome blocks concurrent sends and restart", func(t *testing.T) {
		id := uuid.MustParse(v.Statistics[0].ID)
		if _, err := a.db.Pool.Exec(context.Background(), `UPDATE ord_statistics SET sync_status='pending' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		before := a.ord.Calls(ord.OpStatistics)
		a.ord.TimeoutNext(ord.OpStatistics)
		ref := markingdomain.ObjectRef{Type: markingdomain.TypeStatistics, ID: id}
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.mods.marking.Sync(context.Background(), ref, 1); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		restarted := *a.mods.marking
		if err := restarted.Sync(context.Background(), ref, 2); err != nil {
			t.Fatal(err)
		}
		if a.ord.Calls(ord.OpStatistics) != before+1 {
			t.Fatal("uncertain write replayed")
		}
		if n := a.count(`SELECT count(*) FROM ord_statistics WHERE submission_uncertain AND sync_status='error'`); n != 1 {
			t.Fatal("checkpoint missing")
		}
		p := markingdomain.Period{Year: 2026, Month: time.October}
		if _, err := a.mods.marking.SendReport(context.Background(), p, nil, ""); err == nil {
			t.Fatal("SendReport bypassed uncertainty")
		}
		if r := senior.do(http.MethodPost, "/admin/ord/reports/2026-10/rebuild", map[string]any{"reason": "must not bypass uncertainty"}); r.status != http.StatusConflict {
			t.Fatalf("rebuild uncertainty: %d %s", r.status, r.body)
		}
	})
	t.Run("late document rejection blocks creative links", func(t *testing.T) {
		ctx := context.Background()
		id := v.Invoices[0].ID
		a.ord.RejectObject(id, markingdomain.OrdErirAsyncError, "late test rejection")
		if _, err := a.db.Pool.Exec(ctx, "UPDATE ord_invoice SET sync_status='sent' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE ord_invoice_creatives SET sync_status='pending' WHERE ord_invoice_id=$1", id); err != nil {
			t.Fatal(err)
		}
		before := a.ord.Calls(ord.OpInvoiceCreatives)
		for range 4 {
			_ = a.mods.marking.Poll(ctx, markingdomain.ObjectRef{Type: markingdomain.TypeInvoice, ID: uuid.MustParse(id)})
		}
		_ = a.mods.marking.Sync(ctx, markingdomain.ObjectRef{Type: markingdomain.TypeInvoiceCreatives, ID: uuid.MustParse(id)}, 1)
		if a.ord.Calls(ord.OpInvoiceCreatives) != before {
			t.Fatal("links submitted after document rejection")
		}
		if a.count("SELECT count(*) FROM ord_invoice WHERE sync_status='error'") != 1 {
			t.Fatal("late rejection lost")
		}
		a.ord.ClearReject(id)
		if _, err := a.db.Pool.Exec(ctx, "UPDATE ord_invoice SET sync_status='synced' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Pool.Exec(ctx, "UPDATE ord_invoice_creatives SET sync_status='synced' WHERE ord_invoice_id=$1", id); err != nil {
			t.Fatal(err)
		}
	})

}

// Cancellation can be wrapped as permanent by outbound after the provider accepted it.
type canceledReportAPI struct{ markingdomain.OrdAPI }

func (a canceledReportAPI) UpsertStatistics(ctx context.Context, r markingdomain.StatisticsRequest) (markingdomain.Receipt, error) {
	_, err := a.OrdAPI.UpsertStatistics(ctx, r)
	if err != nil {
		return markingdomain.Receipt{}, err
	}
	return markingdomain.Receipt{}, job.Permanent(context.Canceled)
}
func (a canceledReportAPI) UpsertInvoice(ctx context.Context, r markingdomain.InvoiceRequest) (markingdomain.Receipt, error) {
	_, err := a.OrdAPI.UpsertInvoice(ctx, r)
	if err != nil {
		return markingdomain.Receipt{}, err
	}
	return markingdomain.Receipt{}, job.Permanent(context.Canceled)
}
func (a canceledReportAPI) UpsertInvoiceCreatives(ctx context.Context, r markingdomain.InvoiceRequest) (markingdomain.Receipt, error) {
	_, err := a.OrdAPI.UpsertInvoiceCreatives(ctx, r)
	if err != nil {
		return markingdomain.Receipt{}, err
	}
	return markingdomain.Receipt{}, job.Permanent(context.Canceled)
}
