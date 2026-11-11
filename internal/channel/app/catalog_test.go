package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/money"
)

var f124 = []domain.Price{{FormatID: "f_1_24", Amount: 300000, IsEnabled: true}}

// moderated — канал на модерации с категорией и ценой f_1_24.
func (e *env) moderated(chatID int64) *domain.Channel {
	e.t.Helper()
	c, _ := e.linkedChannel(chatID, allRights)
	cat := channeltest.CategoryFood
	if _, err := e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, domain.ProfileUpdate{CategoryID: &cat}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.SetPrices(e.ctx(), *c.OrganizationID, c.ID, f124); err != nil {
		e.t.Fatal(err)
	}
	c = e.channel(chatID)
	if c.Status != domain.StatusModeration {
		e.t.Fatalf("channel %s, want moderation", c.Status)
	}
	return c
}

// admin — пользователь-администратор и context его действий.
func (e *env) admin() (uuid.UUID, context.Context) {
	e.t.Helper()
	id := ids.New()
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO "user" (id, email, admin_role, status, created_at, updated_at)
		VALUES ($1, $2, 'moderator', 'active', now(), now())`, id, id.String()+"@admin.test"); err != nil {
		e.t.Fatal(err)
	}
	return id, audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdmin, UserID: &id})
}

// active — одобренный канал каталога.
func (e *env) active(chatID int64) *domain.Channel {
	e.t.Helper()
	c := e.moderated(chatID)
	adminID, ctx := e.admin()
	if _, err := e.svc.Approve(ctx, adminID, c.ID); err != nil {
		e.t.Fatal(err)
	}
	return e.channel(chatID)
}

func reason(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("not an app error: %v", err)
	}
	r, _ := ae.Details["reason"].(string)
	return string(ae.Code) + "/" + r
}

func TestPrivateChannelApprovalPersists(t *testing.T) {
	e := newEnv(t)
	c := e.moderated(-80000000100)
	closed := publicCh
	closed.IsPublic = false
	e.max.set(c.MaxChatID, closed, allRights)
	e.event(rightsChanged(c.MaxChatID))
	e.drain()
	adminID, ctx := e.admin()
	if _, err := e.svc.Approve(ctx, adminID, c.ID); err != nil {
		t.Fatal(err)
	}
	c = e.channel(c.MaxChatID)
	if c.Status != domain.StatusActive || c.IsPublic {
		t.Fatalf("persisted approval: status=%s public=%v", c.Status, c.IsPublic)
	}
	if _, err := e.svc.CatalogChannel(context.Background(), c.ID); err != nil {
		t.Fatalf("private channel absent from catalog: %v", err)
	}
	page, err := e.svc.ListCatalog(context.Background(), app.CatalogRequest{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != c.ID {
		t.Fatalf("private catalog: %+v %v", page, err)
	}
}

func TestSetPrices(t *testing.T) {
	e := newEnv(t)
	c := e.moderated(-80000000001)
	org := *c.OrganizationID

	got, err := e.svc.SetPrices(e.ctx(), org, c.ID, []domain.Price{{FormatID: "f_2_48", Amount: 450000, IsEnabled: true}})
	if err != nil || len(got.Prices) != 1 || got.Prices[0].FormatID != "f_2_48" {
		t.Fatalf("replace: %+v %v", got, err)
	}
	if n := e.count(`SELECT count(*) FROM channel_price WHERE channel_id = $1 AND format_id = 'f_1_24'`, c.ID); n != 0 {
		t.Fatal("excluded format kept")
	}
	// moderation: все цены выключены — можно.
	if _, err := e.svc.SetPrices(e.ctx(), org, c.ID, []domain.Price{{FormatID: "f_2_48", Amount: 450000}}); err != nil {
		t.Fatalf("moderation all off: %v", err)
	}
	if _, err := e.svc.SetPrices(e.ctx(), org, c.ID, []domain.Price{{FormatID: "f_1_24", Amount: 9999, IsEnabled: true}}); reason(t, err) != "validation_failed/out_of_range" {
		t.Fatalf("99,99 ₽: %v", err)
	}
	if _, err := e.svc.SetPrices(e.ctx(), e.org(), c.ID, f124); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign channel: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.prices_updated' AND object_id = $1
		AND data -> 'after' ->> 'f_2_48' = '450000 on'`, c.ID.String()); n != 1 {
		t.Fatalf("audit entries with the new price: %d", n)
	}

	a := e.active(-80000000002)
	if _, err := e.svc.SetPrices(e.ctx(), *a.OrganizationID, a.ID, []domain.Price{{FormatID: "f_1_24", Amount: 300000}}); reason(t, err) != "validation_failed/active_requires_price" {
		t.Fatalf("active all off: %v", err)
	}
	if got := e.channel(-80000000002); !got.HasOfferedPrice() {
		t.Fatal("rejected change was saved")
	}

	e.event(botRemoved(-80000000002))
	e.drain()
	if _, err := e.svc.SetPrices(e.ctx(), *a.OrganizationID, a.ID, f124); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("disconnected: %v", err)
	}
}

func TestUnavailability(t *testing.T) {
	e := newEnv(t)
	c := e.moderated(-80000000011)
	org := *c.OrganizationID
	day := 24 * time.Hour
	iv := domain.Unavailability{StartsAt: start.Add(day), EndsAt: start.Add(3 * day)}

	// Два одновременных пересекающихся интервала: принят один.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.AddUnavailability(e.ctx(), org, c.ID, domain.Unavailability{StartsAt: iv.StartsAt.Add(time.Duration(i) * time.Hour), EndsAt: iv.EndsAt})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case reason(t, err) != "validation_failed/overlap":
			t.Fatalf("parallel: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("accepted %d of 2 overlapping intervals", ok)
	}
	if _, err := e.svc.AddUnavailability(e.ctx(), org, c.ID, domain.Unavailability{StartsAt: start.Add(-2 * day), EndsAt: start.Add(-day)}); reason(t, err) != "validation_failed/in_past" {
		t.Fatalf("past: %v", err)
	}
	live, _ := e.svc.Unavailability(context.Background(), c.ID)
	if len(live) != 1 {
		t.Fatalf("live %+v", live)
	}
	// Интервал закончился — в ответах его нет.
	e.clk.Advance(4 * day)
	if live, _ := e.svc.Unavailability(context.Background(), c.ID); len(live) != 0 {
		t.Fatalf("ended interval shown: %+v", live)
	}
	if _, err := e.svc.DeleteUnavailability(e.ctx(), e.org(), c.ID, live0(t, e, c.ID)); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if _, err := e.svc.DeleteUnavailability(e.ctx(), org, c.ID, live0(t, e, c.ID)); err != nil {
		t.Fatalf("delete ended interval: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action IN ('channel.unavailability_added', 'channel.unavailability_deleted')`); n != 2 {
		t.Fatalf("audit entries %d", n)
	}
}

func live0(t *testing.T, e *env, channelID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT id FROM channel_unavailability WHERE channel_id = $1`, channelID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCatalogScenario(t *testing.T) {
	e := newEnv(t)
	a := e.active(-80000000021)
	ctx := context.Background()

	page, err := e.svc.ListCatalog(ctx, app.CatalogRequest{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID || page.NextCursor != "" {
		t.Fatalf("catalog %+v %v", page, err)
	}
	// 300 000 + floor(300 000 × 1500 / 10000) = 345 000.
	if p := page.Items[0].Prices; len(p) != 1 || p[0].Total != 345000 {
		t.Fatalf("price %+v", p)
	}
	card, err := e.svc.CatalogChannel(ctx, a.ID)
	if err != nil || card.ID != a.ID || len(card.History) != 0 {
		t.Fatalf("card %+v %v", card, err)
	}

	for name, r := range map[string]app.CatalogRequest{
		"garbage cursor": {Cursor: "!!"},
		"other sort":     {Sort: domain.SortPriceAsc, Cursor: domain.CatalogCursor{Sort: domain.SortSubscribersDesc, ID: a.ID}.Encode()},
		"price range":    {Filter: domain.CatalogFilter{PriceFrom: ptr[money.Kopecks](2), PriceTo: ptr[money.Kopecks](1)}},
		"unknown format": {Filter: domain.CatalogFilter{FormatID: "f_9_99"}},
	} {
		if _, err := e.svc.ListCatalog(ctx, r); !apperr.Is(err, apperr.CodeValidationFailed) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Права пропали → suspended → канала нет ни в выдаче, ни в карточке.
	e.max.set(-80000000021, publicCh, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	e.event(rightsChanged(-80000000021))
	e.drain()
	if page, _ := e.svc.ListCatalog(ctx, app.CatalogRequest{}); len(page.Items) != 0 {
		t.Fatalf("suspended channel listed")
	}
	if _, err := e.svc.CatalogChannel(ctx, a.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("suspended card: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestModeration(t *testing.T) {
	e := newEnv(t)
	adminID, ctx := e.admin()

	c := e.moderated(-80000000031)
	view, err := e.svc.Approve(ctx, adminID, c.ID)
	if err != nil || view.Channel.Status != domain.StatusActive || view.Channel.ModeratedAt == nil ||
		view.Channel.ModeratedBy == nil || *view.Channel.ModeratedBy != adminID || view.Moderation[0].Action != "approved" {
		t.Fatalf("approve: %+v %v", view, err)
	}
	if page, _ := e.svc.ListCatalog(context.Background(), app.CatalogRequest{}); len(page.Items) != 1 {
		t.Fatal("approved channel not in catalog")
	}
	if _, err := e.svc.Approve(ctx, adminID, c.ID); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("second approve: %v", err)
	}

	noPrice, _ := e.linkedChannel(-80000000032, allRights)
	cat := channeltest.CategoryFood
	if _, err := e.svc.Update(e.ctx(), *noPrice.OrganizationID, noPrice.ID, domain.ProfileUpdate{CategoryID: &cat}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Approve(ctx, adminID, noPrice.ID)
	if ae, ok := apperr.As(err); !ok || ae.Code != apperr.CodeConflict || !slices.Equal(ae.Details["missing"].([]string), []string{"price"}) {
		t.Fatalf("no price: %v", err)
	}

	big := e.moderated(-80000000033)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET subscribers_count = 12000 WHERE id = $1`, big.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Approve(ctx, adminID, big.ID)
	if ae, ok := apperr.As(err); !ok || !slices.Contains(ae.Details["missing"].([]string), "rkn") {
		t.Fatalf("12000 without rkn: %v", err)
	}

	// Отказ → rejected с причиной; правка блогером → снова moderation.
	rej := e.moderated(-80000000034)
	view, err = e.svc.Reject(ctx, adminID, rej.ID, "Нет тематики")
	if err != nil || view.Channel.Status != domain.StatusRejected || view.Channel.StatusReason != "Нет тематики" {
		t.Fatalf("reject: %+v %v", view, err)
	}
	desc := "Рецепты на каждый день"
	if got, err := e.svc.Update(e.ctx(), *rej.OrganizationID, rej.ID, domain.ProfileUpdate{Description: &desc}); err != nil || got.Status != domain.StatusModeration {
		t.Fatalf("after edit: %v", err)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.rejected' AND actor_type = 'admin' AND reason = 'Нет тематики'`) != 1 {
		t.Fatal("rejection not in audit_log")
	}
	if containsLog(e, "Нет тематики") {
		t.Fatal("rejection reason written to the app log")
	}
}

func containsLog(e *env, s string) bool { return strings.Contains(e.logs.String(), s) }

func TestRknCheck(t *testing.T) {
	e := newEnv(t)
	adminID, ctx := e.admin()
	a := e.active(-80000000041)
	org := *a.OrganizationID

	// Подписчиков стало 12 000 → refresh_channel_info приостанавливает канал (R-16).
	grown := publicCh
	grown.SubscribersCount = 12000
	e.max.set(-80000000041, grown, allRights)
	e.event(rightsChanged(-80000000041))
	e.drain()
	if c := e.channel(-80000000041); c.Status != domain.StatusSuspended || c.StatusReason != domain.ReasonRknRequired {
		t.Fatalf("after growth: %s/%s", c.Status, c.StatusReason)
	}
	if len(e.avail.lost) != 1 {
		t.Fatalf("availability lost calls %d", len(e.avail.lost))
	}
	if _, err := e.svc.SetRknCheck(ctx, adminID, a.ID, domain.RknCheckConfirmed, ""); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("check without number: %v", err)
	}
	num := "4930000000"
	if _, err := e.svc.Update(e.ctx(), org, a.ID, domain.ProfileUpdate{RknNumber: &num}); err != nil {
		t.Fatal(err)
	}
	view, err := e.svc.SetRknCheck(ctx, adminID, a.ID, domain.RknCheckRejected, "нет в реестре")
	if err != nil || view.Channel.Status != domain.StatusSuspended || view.Channel.RknStatus != domain.RknRejected {
		t.Fatalf("rkn rejected: %+v %v", view, err)
	}
	view, err = e.svc.SetRknCheck(ctx, adminID, a.ID, domain.RknCheckConfirmed, "")
	if err != nil || view.Channel.Status != domain.StatusActive || len(e.avail.restored) != 1 || view.RknRecheckDue {
		t.Fatalf("rkn confirmed: %+v %v", view, err)
	}
	e.clk.Advance(2160*time.Hour + time.Minute)
	if view, _ := e.svc.AdminGet(context.Background(), a.ID); !view.RknRecheckDue {
		t.Fatal("recheck not due after 90 days + 1 min")
	}
	due := true
	page, err := e.svc.AdminList(context.Background(), app.AdminListRequest{RknRecheckDue: &due})
	if err != nil || len(page.Items) != 1 || page.Items[0].Channel.ID != a.ID {
		t.Fatalf("recheck queue %+v %v", page, err)
	}
}

func TestMetrics(t *testing.T) {
	e := newEnv(t)
	a := e.active(-80000000051)
	mod := e.moderated(-80000000052)
	pending, _ := e.linkedChannel(-80000000053, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	views := 900
	e.max.messages[-80000000051] = []domain.MessageStat{{ID: "m1", At: start.Add(-48 * time.Hour), Views: &views}}

	if err := e.svc.ScheduleMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	var runAts []time.Time
	rows, err := e.d.Pool.Query(context.Background(), `SELECT payload::text, run_at FROM job WHERE kind = $1`, app.KindRefreshChannelMetrics)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []string
	for rows.Next() {
		var p string
		var at time.Time
		if err := rows.Scan(&p, &at); err != nil {
			t.Fatal(err)
		}
		payloads, runAts = append(payloads, p), append(runAts, at)
	}
	if len(payloads) != 2 || strings.Contains(payloads[0]+payloads[1], pending.ID.String()) {
		t.Fatalf("scheduled %v", payloads)
	}
	for _, at := range runAts {
		dayStart := domain.MetricsDayStart(domain.MetricsDate(start))
		if at.Before(dayStart) || !at.Before(dayStart.Add(24*time.Hour)) {
			t.Fatalf("run_at %v outside the day", at)
		}
	}
	if runAts[0].Equal(runAts[1]) {
		t.Log("two channels got the same offset (possible, but unlikely)")
	}
	if err := e.svc.ScheduleMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind = $1`, app.KindRefreshChannelMetrics); n != 2 {
		t.Fatalf("repeat schedule: %d jobs", n)
	}

	p := app.MetricsPayload{ChannelID: a.ID, Date: "2026-10-16"}
	if err := e.svc.RefreshMetrics(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RefreshMetrics(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id = $1 AND avg_views = 900`, a.ID); n != 1 {
		t.Fatalf("snapshots %d", n)
	}

	// Ошибка чтения сообщений — повтор, канал не меняется.
	e.max.failMessages = errors.New("max down")
	if err := e.svc.RefreshMetrics(context.Background(), app.MetricsPayload{ChannelID: mod.ID, Date: "2026-10-16"}); err == nil || job.IsPermanent(err) {
		t.Fatalf("messages error: %v", err)
	}
	e.max.failMessages = nil

	// 9 999 → 10 000 подписчиков без номера → suspended/rkn_required, канал пропал из каталога.
	grown := publicCh
	grown.SubscribersCount = 10000
	e.max.set(-80000000051, grown, allRights)
	if err := e.svc.RefreshMetrics(context.Background(), app.MetricsPayload{ChannelID: a.ID, Date: "2026-10-17"}); err != nil {
		t.Fatal(err)
	}
	if c := e.channel(-80000000051); c.Status != domain.StatusSuspended || c.StatusReason != domain.ReasonRknRequired {
		t.Fatalf("after 10000: %s/%s", c.Status, c.StatusReason)
	}
	if page, _ := e.svc.ListCatalog(context.Background(), app.CatalogRequest{}); len(page.Items) != 0 {
		t.Fatal("rkn_required channel in catalog")
	}
	if len(e.avail.lost) != 1 || e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.status_changed' AND actor_type = 'system'
		AND data ->> 'reason' = 'rkn_required'`) != 1 {
		t.Fatal("availability listener or audit not called")
	}

	// Канал отключён до запуска — MAX не вызывается.
	e.event(botRemoved(-80000000052))
	e.drain()
	calls := e.max.listCalls
	if err := e.svc.RefreshMetrics(context.Background(), app.MetricsPayload{ChannelID: mod.ID, Date: "2026-10-16"}); err != nil {
		t.Fatal(err)
	}
	if e.max.listCalls != calls {
		t.Fatal("MAX called for disconnected channel")
	}
}
