package bootstrap

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
)

// Тексты, которых не должно быть в журнале (07 §5.4).
const (
	e2ePostText       = "Секретный текст осенней распродажи"
	e2eBloggerComment = "Секретный комментарий блогера"
)

// savePost сохраняет текстовый черновик кампании.
func (a *testApp) savePost(adv *client, campaign, text string) {
	a.t.Helper()
	doc := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": text + " https://shop.example.test/sale"}}}}}
	if r := adv.do(http.MethodPut, "/campaigns/"+campaign+"/post", map[string]any{"content_doc": doc, "media_file_ids": []string{}}); r.status != http.StatusOK {
		a.t.Fatalf("post: %d %s", r.status, r.body)
	}
}

func (c *client) order(t *testing.T, id string) orderView {
	t.Helper()
	var o orderView
	r := c.do(http.MethodGet, "/orders/"+id, nil)
	if r.status != http.StatusOK {
		t.Fatalf("order: %d %s", r.status, r.body)
	}
	r.json(t, &o)
	return o
}

func (c *client) bloggerPlacements(t *testing.T) map[string]bloggerPlacementView {
	t.Helper()
	var list struct {
		Items []bloggerPlacementView `json:"items"`
	}
	c.do(http.MethodGet, "/blogger/placements", nil).json(t, &list)
	out := map[string]bloggerPlacementView{}
	for _, p := range list.Items {
		out[p.ID] = p
	}
	return out
}

// placementOf — размещение заказа по каналу.
func (a *testApp) placementOf(orderID, channelID string) string {
	a.t.Helper()
	var id string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT id::text FROM placement WHERE order_id = $1 AND channel_id = $2`,
		orderID, channelID).Scan(&id); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func TestOrderPlacementsEndToEnd(t *testing.T) {
	a := newTestApp(t)
	mod := a.adminClient("moderator.orders@example.ru", "moderator")
	adv := a.advertiserClient("adv.e2e@example.ru")
	blg1 := a.bloggerClient("blogger1.e2e@example.ru")
	blg2 := a.bloggerClient("blogger2.e2e@example.ru")
	var advOrg struct {
		Name string `json:"name"`
		INN  string `json:"inn"`
	}
	adv.do(http.MethodGet, "/organizations/current", nil).json(t, &advOrg)

	// Канал первого блогера подключён через MAX и одобрен; два канала второго — в обход модерации.
	const chat1 = -71000000001
	ch1 := a.moderatedChannel(blg1, chat1, 5000)
	if r := mod.do(http.MethodPost, "/admin/channels/"+ch1+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	a.ordSettle() // площадка в ОРД: канал виден в каталоге (R-14 этапа «Маркировка»)
	ch2 := a.activeChannel(blg2, "Второй", 1_000_000)
	ch3 := a.activeChannel(blg2, "Третий", 1_000_000)

	// S-03 → S-06: корзина из трёх каналов двух блогеров; блогер меняет цену — оформление 409.
	for _, ch := range []string{ch1, ch2, ch3} {
		if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
			t.Fatalf("cart %s: %d %s", ch, r.status, r.body)
		}
	}
	campaign := a.campaignWithPost(adv, "Осенняя распродажа")
	a.savePost(adv, campaign, e2ePostText)
	if r := blg1.do(http.MethodPut, "/blogger/channels/"+ch1+"/prices", map[string]any{
		"prices": []map[string]any{{"format_id": "f_1_24", "amount": 400000, "is_enabled": true}},
	}); r.status != http.StatusOK {
		t.Fatalf("new price: %d %s", r.status, r.body)
	}
	r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil)
	if code, details := r.errorCode(t); r.status != http.StatusConflict || code != "cart_changed" || len(details["items"].([]any)) != 1 {
		t.Fatalf("cart changed: %d %s", r.status, r.body)
	}
	var cart struct {
		Items []struct {
			ID           string `json:"id"`
			PriceChanged bool   `json:"price_changed"`
		} `json:"items"`
	}
	adv.do(http.MethodGet, "/cart", nil).json(t, &cart)
	for _, it := range cart.Items {
		if it.PriceChanged {
			if r := adv.do(http.MethodPatch, "/cart/items/"+it.ID, map[string]any{"format_id": "f_1_24"}); r.status != http.StatusOK {
				t.Fatalf("confirm price: %d %s", r.status, r.body)
			}
		}
	}
	r = adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil)
	if r.status != http.StatusCreated {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	var order orderView
	r.json(t, &order)
	if order.Status != "moderation" || len(order.Placements) != 3 || order.TotalAmount != 460_000+2*1_150_000 {
		t.Fatalf("order %+v", order)
	}
	var camp struct {
		Status  string `json:"status"`
		OrderID string `json:"order_id"`
	}
	adv.do(http.MethodGet, "/campaigns/"+campaign, nil).json(t, &camp)
	if camp.Status != "ordered" || camp.OrderID != order.ID {
		t.Fatalf("campaign %+v", camp)
	}
	var card struct {
		InCart bool `json:"in_cart"`
	}
	adv.do(http.MethodGet, "/catalog/channels/"+ch1, nil).json(t, &card)
	if card.InCart {
		t.Fatal("in_cart after checkout")
	}
	p1, p2, p3 := a.placementOf(order.ID, ch1), a.placementOf(order.ID, ch2), a.placementOf(order.ID, ch3)

	// S-17: одобрение — marking; erid — запросы блогерам с постом.
	version := uuidOf(t, order.CurrentPostVersion.ID)
	v, _ := a.mods.post.Versions.Get(context.Background(), version)
	if _, err := a.mods.post.ApproveVersion(context.Background(), version, v.CreatedBy); err != nil {
		t.Fatal(err)
	}
	if o := adv.order(t, order.ID); o.Status != "marking" {
		t.Fatalf("after approve: %s", o.Status)
	}
	if _, err := a.mods.post.MarkVersionReady(context.Background(), version, "2Vtz-e2e"); err != nil {
		t.Fatal(err)
	}
	if o := adv.order(t, order.ID); o.Status != "in_progress" {
		t.Fatalf("after ready: %s", o.Status)
	}
	got := blg1.bloggerPlacements(t)[p1]
	if got.Status != "awaiting_blogger" || got.Post == nil || got.Post.Erid != "2Vtz-e2e" || !strings.Contains(got.Post.FinalText, "erid") {
		t.Fatalf("blogger 1 request %+v", got)
	}

	// S-14: первый блогер принимает и планирует; второй отказывается от одного, второе истекает по T-01.
	if r := blg1.do(http.MethodPost, "/blogger/placements/"+p1+"/accept", nil); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if r := blg1.do(http.MethodPost, "/blogger/placements/"+p1+"/accept", nil); r.status != http.StatusConflict {
		t.Fatalf("accept twice: %d", r.status)
	}
	if r := blg1.do(http.MethodPost, "/blogger/placements/"+p1+"/schedule", map[string]any{"mode": "at",
		"publish_at": "2026-10-20T09:00:00Z"}); r.status != http.StatusOK {
		t.Fatalf("schedule: %d %s", r.status, r.body)
	}
	if r := blg2.do(http.MethodPost, "/blogger/placements/"+p2+"/decline", map[string]any{"comment": e2eBloggerComment}); r.status != http.StatusOK {
		t.Fatalf("decline: %d %s", r.status, r.body)
	}
	a.clk.Advance(24*time.Hour + time.Minute)
	a.settle()
	if st := blg2.bloggerPlacements(t)[p3].Status; st != "expired" {
		t.Fatalf("T-01: %s", st)
	}

	// S-29: правка поста — запланированное размещение возвращается в created, задания отменены.
	a.savePost(adv, campaign, e2ePostText+" (исправлено)")
	r = adv.do(http.MethodPost, "/orders/"+order.ID+"/post-versions", nil)
	if r.status != http.StatusCreated {
		t.Fatalf("post version: %d %s", r.status, r.body)
	}
	var edited orderView
	r.json(t, &edited)
	if edited.Status != "moderation" || edited.CurrentPostVersion.ID == order.CurrentPostVersion.ID {
		t.Fatalf("edited order %+v", edited)
	}
	var cancelledJobs int
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE dedupe_key IN ($1, $2) AND status = 'cancelled'`,
		"publish:"+p1, "remove:"+p1).Scan(&cancelledJobs); err != nil || cancelledJobs != 2 {
		t.Fatalf("publish jobs cancelled %d %v", cancelledJobs, err)
	}
	if st := blg1.bloggerPlacements(t)[p1]; st.Status != "created" || st.Post != nil {
		t.Fatalf("after P-11: %+v", st)
	}
	a.readyVersion(edited.CurrentPostVersion.ID)
	if st := blg1.bloggerPlacements(t)[p1].Status; st != "awaiting_blogger" {
		t.Fatalf("after new version ready: %s", st)
	}

	// S-25: первый блогер принял, канал теряет права — размещение отменено.
	if r := blg1.do(http.MethodPost, "/blogger/placements/"+p1+"/accept", nil); r.status != http.StatusOK {
		t.Fatalf("accept again: %d %s", r.status, r.body)
	}
	a.maxFake().SetMembership(chat1, max.Membership{IsAdmin: true, Permissions: []string{"write"}})
	a.maxEvent(rightsEvent(chat1))
	a.settle()
	final := adv.order(t, order.ID)
	statuses := map[string]string{}
	for _, p := range final.Placements {
		statuses[p.ID] = p.Status + "/" + p.StatusReason
	}
	if statuses[p1] != "cancelled/channel_unavailable" || statuses[p2] != "declined/blogger_declined" ||
		statuses[p3] != "expired/accept_timeout" || final.Status != "completed" {
		t.Fatalf("final %v %s", statuses, final.Status)
	}
	adv.do(http.MethodGet, "/campaigns/"+campaign, nil).json(t, &camp)
	if camp.Status != "archived" {
		t.Fatalf("campaign after completion: %s", camp.Status)
	}

	// S-07: второй заказ отменяется целиком.
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch2, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("cart: %d", r.status)
	}
	second := a.campaignWithPost(adv, "Зимняя распродажа")
	var order2 orderView
	adv.do(http.MethodPost, "/campaigns/"+second+"/checkout", nil).json(t, &order2)
	r = adv.do(http.MethodPost, "/orders/"+order2.ID+"/cancel", nil)
	var cancelled orderView
	r.json(t, &cancelled)
	if r.status != http.StatusOK || cancelled.Status != "cancelled" || cancelled.Placements[0].Status != "cancelled" {
		t.Fatalf("cancel order: %d %s", r.status, r.body)
	}
	adv.do(http.MethodGet, "/campaigns/"+second, nil).json(t, &camp)
	if camp.Status != "archived" {
		t.Fatalf("cancelled order campaign: %s", camp.Status)
	}

	// История: переходы по порядку, с актором; у таймера — задание.
	wantHistory := map[string]string{
		p1: "P-01:advertiser,P-02:system,P-03:blogger,P-17:blogger,P-11:advertiser,P-02:system,P-03:blogger,P-06:system",
		p2: "P-01:advertiser,P-02:system,P-04:blogger",
		p3: "P-01:advertiser,P-02:system,P-05:system",
	}
	for _, p := range final.Placements {
		var seq []string
		for _, h := range p.History {
			seq = append(seq, h.Transition+":"+h.ActorType)
		}
		if got := strings.Join(seq, ","); got != wantHistory[p.ID] {
			t.Errorf("history %s: %s, want %s", p.ID, got, wantHistory[p.ID])
		}
	}
	var timerJobs int
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM placement_state_history WHERE placement_id = $1
		AND transition = 'P-05' AND job_id IS NOT NULL`, p3).Scan(&timerJobs); err != nil || timerJobs != 1 {
		t.Fatalf("T-01 history source: %d %v", timerJobs, err)
	}

	// Журнал: обязательные события этапа (07 §5.3) есть; текст поста, комментарий блогера,
	// название и ИНН рекламодателя — нет (07 §5.4).
	logs := a.logs.String()
	for _, m := range []string{"placement transition applied", "placement transition rejected", "placement timer fired",
		"job started", "job finished", "order created", "checkout rejected", "order status changed",
		"post version change applied", "placements cancelled on channel loss", "order post version created"} {
		if !strings.Contains(logs, `"msg":"`+m+`"`) {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range []string{e2ePostText, e2eBloggerComment, advOrg.Name, advOrg.INN} {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("log contains %q", s)
		}
	}
}
