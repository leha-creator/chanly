package bootstrap

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/platform/inbound"
)

func TestAssembledApp(t *testing.T) {
	a := newTestApp(t)
	resp, err := http.Get(a.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz: %d", resp.StatusCode)
	}
	for _, kind := range []string{"send_login_code", "process_inbound_event", "send_alert", "check_stuck_objects", "cleanup",
		"refresh_channel_info", "delete_max_message", "leave_max_chat", "reply_bot_dialog", "release_unclaimed_channels",
		"schedule_channel_metrics", "refresh_channel_metrics",
		"build_media_derivative", "publish_media_derivative", "purge_media", "send_notification", "placement_reminder"} {
		if !slices.Contains(a.worker.Kinds(), kind) {
			t.Fatalf("worker kinds %v have no %s", a.worker.Kinds(), kind)
		}
	}
	resp, err = http.Get(a.srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi.json outside production: %d", resp.StatusCode)
	}
}

func TestMAXEventHandlersRegistered(t *testing.T) {
	a := newTestApp(t)
	for _, ev := range []string{"bot_added", "bot_removed", "bot_admin_permissions_changed", "message_created",
		"message_edited", "message_removed", "chat_title_changed"} {
		if !a.mods.platform.dispatcher.Handles(inbound.SourceMAX, ev) {
			t.Errorf("no handler for MAX event %s", ev)
		}
	}
}

func TestSubscribeMAX(t *testing.T) {
	fake := maxfake.New()
	ctx := t.Context()
	url := "https://api.maxim.example/api/v1/webhooks/max/secret"
	for range 2 {
		if err := subscribeMAX(ctx, fake, url, "s"); err != nil {
			t.Fatal(err)
		}
	}
	subs, _ := fake.ListSubscriptions(ctx)
	if len(subs) != 1 || subs[0].URL != url || !slices.Contains(subs[0].UpdateTypes, "bot_admin_permissions_changed") {
		t.Fatalf("subscriptions %+v", subs)
	}
	// Подписка без событий о публикациях обновляется (R-17); полная — нет.
	old := []string{"bot_added", "bot_removed", "bot_admin_permissions_changed", "message_created", "chat_title_changed"}
	if err := fake.Subscribe(ctx, url, old, "s"); err != nil {
		t.Fatal(err)
	}
	if err := subscribeMAX(ctx, fake, url, "s"); err != nil {
		t.Fatal(err)
	}
	subs, _ = fake.ListSubscriptions(ctx)
	if len(subs) != 1 || !slices.Contains(subs[0].UpdateTypes, "message_removed") || !slices.Contains(subs[0].UpdateTypes, "message_edited") {
		t.Fatalf("subscription not updated: %+v", subs)
	}
	cfg := Config{MAX: MAXConfig{WebhookURL: "https://api.maxim.example/api/v1/webhooks/max/", WebhookPathSecret: "secret"}}
	if got := maxWebhookURL(cfg); got != url {
		t.Fatalf("webhook url %q", got)
	}
}

// Этап «Маркировка»: виды заданий, слушатели и проверки зависших объектов подключены.
func TestMarkingWiring(t *testing.T) {
	a := newTestApp(t)
	for _, kind := range []string{markingapp.KindSyncOrdObject, markingapp.KindPollOrdStatus} {
		if !slices.Contains(a.worker.Kinds(), kind) {
			t.Fatalf("worker kinds %v have no %s", a.worker.Kinds(), kind)
		}
	}
	if a.mods.marking.Listener != a.mods.placement {
		t.Fatalf("creative listener %T", a.mods.marking.Listener)
	}
	var platform int
	if err := a.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM organization WHERE is_platform`).Scan(&platform); err != nil || platform != 1 {
		t.Fatalf("platform organization rows %d %v", platform, err)
	}

	// Одобренный канал до регистрации площадки не виден; после — виден.
	mod := a.adminClient("moderator.marking@example.ru", "moderator")
	blg := a.bloggerClient("blogger.marking@example.ru")
	ch := a.moderatedChannel(blg, -72000000001, 5000)
	if r := mod.do(http.MethodPost, "/admin/channels/"+ch+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	adv := a.advertiserClient("adv.marking@example.ru")
	if r := adv.do(http.MethodGet, "/catalog/channels/"+ch, nil); r.status != http.StatusNotFound {
		t.Fatalf("card before ord platform: %d", r.status)
	}
	a.ordSettle()
	if r := adv.do(http.MethodGet, "/catalog/channels/"+ch, nil); r.status != http.StatusOK {
		t.Fatalf("card after ord platform: %d %s", r.status, r.body)
	}
	if _, ok := a.ord.Object(ch); !ok {
		t.Fatal("platform not registered in ordfake")
	}
}

// ORD_ENABLED=false (R-18): слушатели маркировки не подключены, одобрение сразу делает канал видимым.
func TestMarkingDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.ORD.Enabled = false
	a := newTestAppWith(t, cfg)
	if slices.Contains(a.worker.Kinds(), markingapp.KindSyncOrdObject) {
		t.Fatal("sync_ord_object registered with ORD disabled")
	}
	if _, ok := a.mods.post.Listener.(bridge.VersionListener); !ok {
		t.Fatalf("post version listener %T", a.mods.post.Listener)
	}
	mod := a.adminClient("moderator.nomarking@example.ru", "moderator")
	blg := a.bloggerClient("blogger.nomarking@example.ru")
	ch := a.moderatedChannel(blg, -72000000002, 5000)
	if r := mod.do(http.MethodPost, "/admin/channels/"+ch+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	adv := a.advertiserClient("adv.nomarking@example.ru")
	if r := adv.do(http.MethodGet, "/catalog/channels/"+ch, nil); r.status != http.StatusOK {
		t.Fatalf("card with ORD disabled: %d %s", r.status, r.body)
	}
}

// Этап «Приём оплаты и учёт денег»: задания денег зарегистрированы, журнал в placement,
// операции тега balance отвечают не 501.
func TestBillingWiring(t *testing.T) {
	a := newTestApp(t)
	for _, kind := range []string{"expire_invoices", "check_ledger_balance", "generate_closing_acts"} {
		if !slices.Contains(a.worker.Kinds(), kind) {
			t.Errorf("worker kinds %v have no %s", a.worker.Kinds(), kind)
		}
	}
	if _, ok := a.mods.placement.Holds.(bridge.LedgerHolds); !ok {
		t.Errorf("placement holds %T, want bridge.LedgerHolds", a.mods.placement.Holds)
	}
	if _, ok := a.mods.billing.Placements.(bridge.BillingPlacements); !ok {
		t.Errorf("billing placements %T", a.mods.billing.Placements)
	}
	adv := a.newAdvertiser("adv-wiring@example.ru")
	id := "0190a000-0000-7000-8000-000000000999"
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/balance"},
		{http.MethodGet, "/balance/transactions"},
		{http.MethodGet, "/invoices"},
		{http.MethodGet, "/invoices/" + id + "/pdf"},
		{http.MethodGet, "/closing-acts"},
		{http.MethodGet, "/closing-acts/" + id + "/act"},
	} {
		if r := adv.do(op.method, op.path, nil); r.status == http.StatusNotImplemented || r.status >= 500 {
			t.Errorf("%s %s: %d", op.method, op.path, r.status)
		}
	}
}

// Этап выплат (I-1, R-11): задания, события Рокет Ворк, гейт исполнителя и зависшие.
func TestPayoutWiring(t *testing.T) {
	a := newTestApp(t)
	for _, kind := range []string{payoutapp.KindEnsureContractor, payoutapp.KindPollContractor, payoutapp.KindSendPayout,
		payoutapp.KindPollPayout, payoutapp.KindCheckDeposit} {
		if !slices.Contains(a.worker.Kinds(), kind) {
			t.Fatalf("worker kinds %v have no %s", a.worker.Kinds(), kind)
		}
	}
	for _, ev := range []string{"contractor_status", "payout_status"} {
		if !a.mods.platform.dispatcher.Handles(inbound.SourceRocketWork, ev) {
			t.Errorf("no handler for Rocket Work event %s", ev)
		}
	}
	if a.mods.placement.Contractors != a.mods.payout {
		t.Fatalf("contractor gate %T", a.mods.placement.Contractors)
	}
	if _, ok := a.mods.placement.Payouts.(bridge.PlacementPayouts); !ok {
		t.Fatalf("payouts port %T", a.mods.placement.Payouts)
	}
	// Вебхук смонтирован: неверная подпись — 401, а не 404 или 501.
	resp, err := http.Post(a.srv.URL+"/api/v1/webhooks/rocketwork", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rocketwork webhook: %d", resp.StatusCode)
	}
}
