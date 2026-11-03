package bootstrap

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// Этап «API администратора»: модерация поста, размещения, споры и переписка, блокировка канала
// и справочник категорий через HTTP (R-1, R-4 … R-11).

type adminOrderView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Advertiser struct {
		Inn string `json:"inn"`
	} `json:"advertiser"`
	CurrentPostVersion struct {
		Status string `json:"status"`
	} `json:"current_post_version"`
	Placements []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"placements"`
}

type adminPlacementView struct {
	ID               string   `json:"id"`
	Status           string   `json:"status"`
	StatusReason     string   `json:"status_reason"`
	RemovalReason    string   `json:"removal_reason"`
	AvailableActions []string `json:"available_actions"`
	Dispute          *struct {
		ID string `json:"id"`
	} `json:"dispute"`
}

type page[T any] struct {
	Items []T  `json:"items"`
	Total *int `json:"total"`
}

// moderationOrder — заказ рекламодателя на модерации поста (версия не одобрена).
func (a *testApp) moderationOrder(adv, blg *client, title string) orderView {
	a.t.Helper()
	ch := a.activeChannel(blg, title, 1_000_000)
	campaign := a.campaignWithPost(adv, "Кампания "+title)
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		a.t.Fatalf("add to cart: %d %s", r.status, r.body)
	}
	var order orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil).json(a.t, &order)
	return order
}

func expectStatus(t *testing.T, r response, want int, what string) {
	t.Helper()
	if r.status != want {
		t.Fatalf("%s: %d, want %d: %s", what, r.status, want, r.body)
	}
}

func TestAdminOrderModerationHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-mod@example.ru")
	blg := a.bloggerClient("blg-mod@example.ru")
	mod := a.adminClient("mod-orders@example.ru", accountdomain.AdminModerator)
	approveOrder := a.moderationOrder(adv, blg, "Модерация 1")
	rejectOrder := a.moderationOrder(adv, blg, "Модерация 2")

	// Не администратор — 403 admin_required.
	r := adv.do(http.MethodGet, "/admin/orders", nil)
	if _, d := r.errorCode(t); r.status != http.StatusForbidden || d["reason"] != "admin_required" {
		t.Fatalf("advertiser: %d %s", r.status, r.body)
	}
	var list page[adminOrderView]
	r = mod.do(http.MethodGet, "/admin/orders", nil)
	expectStatus(t, r, http.StatusOK, "list")
	r.json(t, &list)
	if list.Total == nil || *list.Total != 2 || list.Items[0].Advertiser.Inn == "" || len(list.Items[0].Advertiser.Inn) == 10 &&
		list.Items[0].Advertiser.Inn[2] != '*' {
		t.Fatalf("queue %+v", list)
	}
	var v adminOrderView
	mod.do(http.MethodGet, "/admin/orders/"+approveOrder.ID, nil).json(t, &v)
	if v.Status != "moderation" || v.CurrentPostVersion.Status != "pending_moderation" || len(v.Placements) != 1 {
		t.Fatalf("order before approve: %+v", v)
	}

	// Одобрение: версия одобрена, заказ ждёт маркировки; ОРД выдаёт erid — размещение уходит блогеру.
	r = mod.do(http.MethodPost, "/admin/orders/"+approveOrder.ID+"/approve", nil)
	expectStatus(t, r, http.StatusOK, "approve")
	r.json(t, &v)
	if v.Status != "marking" {
		t.Fatalf("approved order %+v", v)
	}
	expectStatus(t, mod.do(http.MethodPost, "/admin/orders/"+approveOrder.ID+"/approve", nil), http.StatusConflict, "repeat approve")
	if v.CurrentPostVersion.Status != "approved" {
		t.Fatalf("version after approve %+v", v)
	}

	// Отказ без причины — 422; с причиной — заказ rejected.
	expectStatus(t, mod.do(http.MethodPost, "/admin/orders/"+rejectOrder.ID+"/reject", map[string]any{"reason": ""}),
		http.StatusUnprocessableEntity, "reject without reason")
	r = mod.do(http.MethodPost, "/admin/orders/"+rejectOrder.ID+"/reject", map[string]any{"reason": "Нет маркировки «Реклама»"})
	expectStatus(t, r, http.StatusOK, "reject")
	r.json(t, &v)
	if v.Status != "rejected" {
		t.Fatalf("rejected order %+v", v)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action IN ('post.version_approved', 'post.version_rejected') AND actor_type = 'admin'`); n != 2 {
		t.Fatalf("audit %d", n)
	}
	mod.do(http.MethodGet, "/admin/orders?status=all", nil).json(t, &list)
	if *list.Total != 2 {
		t.Fatalf("all orders %d", *list.Total)
	}
}

func TestAdminPlacementActionsHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-place@example.ru")
	blg := a.bloggerClient("blg-place@example.ru")

	_, scheduled := a.scheduledHTTP(adv, blg, "Отмена")
	// Сессия администратора короче: вход после перевода часов.
	mod := a.adminClient("mod-place@example.ru", accountdomain.AdminModerator)
	sen := a.adminClient("sen-place@example.ru", accountdomain.AdminSenior)
	var v adminPlacementView
	r0 := mod.do(http.MethodGet, "/admin/placements/"+scheduled, nil)
	expectStatus(t, r0, http.StatusOK, "get placement")
	r0.json(t, &v)
	if v.Status != "scheduled" || slices.Contains(v.AvailableActions, "cancel") {
		t.Fatalf("moderator view %+v", v)
	}
	sen.do(http.MethodGet, "/admin/placements/"+scheduled, nil).json(t, &v)
	if !slices.Contains(v.AvailableActions, "cancel") {
		t.Fatalf("senior view %+v", v)
	}
	r := mod.do(http.MethodPost, "/admin/placements/"+scheduled+"/cancel", map[string]any{"reason": "Нарушение"})
	if _, d := r.errorCode(t); r.status != http.StatusForbidden || d["reason"] != "admin_required" {
		t.Fatalf("moderator cancel: %d %s", r.status, r.body)
	}
	before := a.available(adv)
	r = sen.do(http.MethodPost, "/admin/placements/"+scheduled+"/cancel", map[string]any{"reason": "Нарушение правил"})
	expectStatus(t, r, http.StatusOK, "cancel")
	r.json(t, &v)
	if v.Status != "cancelled" || v.StatusReason != "admin_cancel" {
		t.Fatalf("cancelled %+v", v)
	}
	if got := a.available(adv) - before; got != 1_150_000 {
		t.Fatalf("hold released %d", got)
	}
	expectStatus(t, sen.do(http.MethodPost, "/admin/placements/"+scheduled+"/cancel", map[string]any{"reason": "ещё"}),
		http.StatusConflict, "repeat cancel")

	// Отметка снятия: время раньше публикации — 422; верное — removed с причиной admin.
	_, published := a.publishedHTTP(adv, blg, "Снятие")
	mod = a.adminClient("mod-place@example.ru", accountdomain.AdminModerator)
	sen = a.adminClient("sen-place@example.ru", accountdomain.AdminSenior)
	sen.do(http.MethodGet, "/admin/placements/"+published, nil).json(t, &v)
	if !slices.Contains(v.AvailableActions, "mark_removed") || !slices.Contains(v.AvailableActions, "remove_post") {
		t.Fatalf("published actions %+v", v)
	}
	expectStatus(t, sen.do(http.MethodPost, "/admin/placements/"+published+"/mark-removed",
		map[string]any{"removed_at": "2026-10-20T08:00:00Z", "reason": "Проверено вручную"}), http.StatusUnprocessableEntity, "early")
	r = sen.do(http.MethodPost, "/admin/placements/"+published+"/mark-removed",
		map[string]any{"removed_at": "2026-10-20T09:00:03Z", "reason": "Пост удалён, проверено вручную"})
	expectStatus(t, r, http.StatusOK, "mark removed")
	r.json(t, &v)
	if v.Status != "removed" || v.RemovalReason != "admin" {
		t.Fatalf("marked removed %+v", v)
	}
	if n := a.count(`SELECT count(*) FROM job WHERE dedupe_key = $1 AND status = 'pending'`, "remove_placement:"+published); n != 0 {
		t.Fatalf("remove job still pending: %d", n)
	}

	var list page[adminPlacementView]
	r = mod.do(http.MethodGet, "/admin/placements?status=removed", nil)
	expectStatus(t, r, http.StatusOK, "list")
	r.json(t, &list)
	if *list.Total != 1 || list.Items[0].ID != published {
		t.Fatalf("filtered %+v", list)
	}
	mod.do(http.MethodGet, "/admin/placements?from=2026-10-01&to=2026-10-31", nil).json(t, &list)
	if *list.Total != 2 {
		t.Fatalf("period %d", *list.Total)
	}
}

type adminDisputeView struct {
	ID          string `json:"id"`
	PlacementID string `json:"placement_id"`
	Resolution  string `json:"resolution"`
	Messages    []struct {
		Text string `json:"text"`
	} `json:"messages"`
	Placement adminPlacementView `json:"placement"`
}

func TestAdminDisputesHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-disp@example.ru")
	blg := a.bloggerClient("blg-disp@example.ru")
	_, pid := a.removedHTTP(adv, blg, "Спор")
	mod := a.adminClient("mod-disp@example.ru", accountdomain.AdminModerator)
	expectStatus(t, adv.do(http.MethodPost, "/placements/"+pid+"/claims", map[string]any{"reason": "content_mismatch", "text": "Не тот текст"}),
		http.StatusCreated, "claim")

	var list page[adminDisputeView]
	r0 := mod.do(http.MethodGet, "/admin/disputes", nil)
	expectStatus(t, r0, http.StatusOK, "list disputes")
	r0.json(t, &list)
	if *list.Total != 1 || list.Items[0].PlacementID != pid {
		t.Fatalf("disputes %+v", list)
	}
	id := list.Items[0].ID

	// Предпросмотр 7500: 7 500,00 / 1 125,00 / 2 875,00 (03 §7) и без изменений.
	var pv struct {
		Blogger int64 `json:"blogger_amount"`
		Fee     int64 `json:"fee_amount"`
		Refund  int64 `json:"refund_amount"`
	}
	r := mod.do(http.MethodGet, "/admin/disputes/"+id+"/preview?resolution=proportional&share=7500", nil)
	expectStatus(t, r, http.StatusOK, "preview")
	r.json(t, &pv)
	if pv.Blogger != 750_000 || pv.Fee != 112_500 || pv.Refund != 287_500 {
		t.Fatalf("preview %+v", pv)
	}
	expectStatus(t, mod.do(http.MethodGet, "/admin/disputes/"+id+"/preview?resolution=proportional", nil),
		http.StatusUnprocessableEntity, "no share")

	// Переписка: сообщение блогеру не видно рекламодателю; ответ стороны — администратору.
	expectStatus(t, mod.do(http.MethodPost, "/admin/disputes/"+id+"/messages", map[string]any{"to": "blogger", "text": "Пришлите скриншот"}),
		http.StatusCreated, "admin message")
	var msgs page[struct {
		Text string `json:"text"`
	}]
	adv.do(http.MethodGet, "/placements/"+pid+"/dispute/messages", nil).json(t, &msgs)
	if len(msgs.Items) != 0 {
		t.Fatalf("advertiser sees %+v", msgs.Items)
	}
	blg.do(http.MethodGet, "/blogger/placements/"+pid+"/dispute/messages", nil).json(t, &msgs)
	if len(msgs.Items) != 1 {
		t.Fatalf("blogger sees %+v", msgs.Items)
	}
	expectStatus(t, blg.do(http.MethodPost, "/blogger/placements/"+pid+"/dispute/messages", map[string]any{"text": "Скриншот приложен"}),
		http.StatusCreated, "blogger reply")
	if n := a.count(`SELECT count(*) FROM notification WHERE template = 'placement.dispute_message'`); n == 0 {
		t.Fatal("no dispute message notifications")
	}

	// Решение по HTTP — те же суммы, что у команды.
	r = mod.do(http.MethodPost, "/admin/disputes/"+id+"/resolve", map[string]any{"resolution": "proportional", "share": 7500,
		"comment": "Пост соответствовал частично"})
	expectStatus(t, r, http.StatusOK, "resolve")
	var d adminDisputeView
	r.json(t, &d)
	if d.Resolution != "proportional" || d.Placement.Status != "accepted" || len(d.Messages) != 2 {
		t.Fatalf("resolved %+v", d)
	}
	if n := a.count(`SELECT count(*) FROM payout WHERE placement_id = $1 AND amount = 750000`, pid); n != 1 {
		t.Fatalf("payout %d", n)
	}
	r = adv.do(http.MethodPost, "/placements/"+pid+"/dispute/messages", map[string]any{"text": "Ещё"})
	if _, det := r.errorCode(t); r.status != http.StatusConflict || det["reason"] != "dispute_resolved" {
		t.Fatalf("message after resolve: %d %s", r.status, r.body)
	}
	// Чужое размещение — 404.
	other := a.advertiserClient("adv-other@example.ru")
	expectStatus(t, other.do(http.MethodGet, "/placements/"+pid+"/dispute/messages", nil), http.StatusNotFound, "foreign")
	a.ledgerOK("dispute by HTTP")
	for _, secret := range []string{"Пришлите скриншот", "Скриншот приложен", "Пост соответствовал частично"} {
		if containsLog(a, secret) {
			t.Fatalf("log contains %q", secret)
		}
	}
}

func containsLog(a *testApp, s string) bool { return strings.Contains(a.logs.String(), s) }

// channelOfPlacement — канал размещения.
func (a *testApp) channelOfPlacement(pid string) string {
	a.t.Helper()
	var id string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT channel_id::text FROM placement WHERE id = $1`, pid).Scan(&id); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func TestAdminChannelBlockAndCategoriesHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-block@example.ru")
	blg := a.bloggerClient("blg-block@example.ru")
	_, pid := a.scheduledHTTP(adv, blg, "Блок")
	mod := a.adminClient("mod-block@example.ru", accountdomain.AdminModerator)
	sen := a.adminClient("sen-block@example.ru", accountdomain.AdminSenior)
	var ch struct {
		ChannelID string `json:"channel_id"`
	}
	ch.ChannelID = a.channelOfPlacement(pid)

	expectStatus(t, mod.do(http.MethodPost, "/admin/channels/"+ch.ChannelID+"/block", map[string]any{"reason": "Жалоба"}),
		http.StatusForbidden, "moderator block")
	r := sen.do(http.MethodPost, "/admin/channels/"+ch.ChannelID+"/block", map[string]any{"reason": "Жалобы подписчиков"})
	expectStatus(t, r, http.StatusOK, "block")
	var c struct {
		Status string `json:"status"`
	}
	r.json(t, &c)
	if c.Status != "blocked" {
		t.Fatalf("blocked %+v", c)
	}
	expectStatus(t, sen.do(http.MethodPost, "/admin/channels/"+ch.ChannelID+"/block", map[string]any{"reason": "ещё"}),
		http.StatusConflict, "repeat block")
	expectStatus(t, adv.do(http.MethodGet, "/catalog/channels/"+ch.ChannelID, nil), http.StatusNotFound, "catalog")
	if st, _ := a.placementState(pid); st != "scheduled" {
		t.Fatalf("placement after block: %s", st)
	}
	a.settle()
	if n := a.count(`SELECT count(*) FROM notification WHERE template = 'channel.blocked'`); n == 0 {
		t.Fatal("no channel.blocked notification")
	}
	r = sen.do(http.MethodPost, "/admin/channels/"+ch.ChannelID+"/unblock", map[string]any{"reason": "Разобрались"})
	expectStatus(t, r, http.StatusOK, "unblock")
	r.json(t, &c)
	if c.Status != "moderation" && c.Status != "pending_rights" {
		t.Fatalf("unblocked %+v", c)
	}

	// Справочник категорий: только senior; повтор slug — 409; архив не виден кабинету.
	expectStatus(t, mod.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": "games", "name": "Игры"}),
		http.StatusForbidden, "moderator category")
	var cat struct {
		ID       string `json:"id"`
		Archived bool   `json:"archived"`
	}
	r = sen.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": "games2", "name": "Игры"})
	expectStatus(t, r, http.StatusCreated, "create category")
	r.json(t, &cat)
	expectStatus(t, sen.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": "games2", "name": "Игры"}),
		http.StatusConflict, "duplicate slug")
	expectStatus(t, sen.do(http.MethodPost, "/admin/channel-categories", map[string]any{"slug": "Игры", "name": "Игры"}),
		http.StatusUnprocessableEntity, "bad slug")
	var cats []struct {
		ID string `json:"id"`
	}
	listCats := func() bool {
		var body struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		adv.do(http.MethodGet, "/channel-categories", nil).json(t, &body)
		cats = body.Items
		return slices.ContainsFunc(cats, func(x struct {
			ID string `json:"id"`
		}) bool {
			return x.ID == cat.ID
		})
	}
	if !listCats() {
		t.Fatal("new category not listed")
	}
	r = sen.do(http.MethodPatch, "/admin/channel-categories/"+cat.ID, map[string]any{"archived": true})
	expectStatus(t, r, http.StatusOK, "archive")
	if listCats() {
		t.Fatal("archived category listed")
	}
}

// TestAdminOrderApproveWithoutMarking — ORD_ENABLED=false (I-2): одобренная версия сразу получает
// erid разработки, размещение уходит блогеру.
func TestAdminOrderApproveWithoutMarking(t *testing.T) {
	cfg := testConfig()
	cfg.ORD.Enabled = false
	a := newTestAppWith(t, cfg)
	adv := a.advertiserClient("adv-noord@example.ru")
	blg := a.bloggerClient("blg-noord@example.ru")
	mod := a.adminClient("mod-noord@example.ru", accountdomain.AdminModerator)
	o := a.moderationOrder(adv, blg, "Без ОРД")
	r := mod.do(http.MethodPost, "/admin/orders/"+o.ID+"/approve", nil)
	expectStatus(t, r, http.StatusOK, "approve")
	var v adminOrderView
	r.json(t, &v)
	if v.Status != "in_progress" || v.CurrentPostVersion.Status != "ready" || v.Placements[0].Status != "awaiting_blogger" {
		t.Fatalf("approved without marking %+v", v)
	}
}
