package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// doKey — запрос с заголовком Idempotency-Key (R-18).
func (c *client) doKey(method, path, key string, body any) response {
	c.app.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.app.srv.URL+"/api/v1"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Idempotency-Key", key)
	u, _ := url.Parse(c.app.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "csrf_token" {
			req.Header.Set("X-CSRF-Token", ck.Value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.app.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := response{status: resp.StatusCode, header: resp.Header, body: raw, req: req}
	c.app.checkResponse(r)
	return r
}

// orgID — организация пользователя.
func (c *client) orgID(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.MustParse(c.me(t).Organizations[0].ID)
}

// activeChannel — активный канал блогера с ценой f_1_24 (в обход модерации).
func (a *testApp) activeChannel(blg *client, title string, amount int64) string {
	a.t.Helper()
	return placementtest.Channel(a.t, a.db.Pool, blg.orgID(a.t), title, money.Kopecks(amount)).String()
}

// campaignWithPost — кампания 20–22 октября 09:00–21:00 МСК с текстовым постом.
func (a *testApp) campaignWithPost(adv *client, name string) string {
	a.t.Helper()
	r := adv.do(http.MethodPost, "/campaigns", map[string]any{
		"name": name, "date_from": "2026-10-20", "date_to": "2026-10-22", "time_from": "09:00", "time_to": "21:00",
	})
	if r.status != http.StatusCreated {
		a.t.Fatalf("campaign: %d %s", r.status, r.body)
	}
	var c struct {
		ID string `json:"id"`
	}
	r.json(a.t, &c)
	doc := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "content": []any{
		map[string]any{"type": "text", "text": "Скидки https://shop.example.test/sale"}}}}}
	if r := adv.do(http.MethodPut, "/campaigns/"+c.ID+"/post", map[string]any{"content_doc": doc, "media_file_ids": []string{}}); r.status != http.StatusOK {
		a.t.Fatalf("post: %d %s", r.status, r.body)
	}
	return c.ID
}

type orderView struct {
	ID                 string `json:"id"`
	Number             int64  `json:"number"`
	Status             string `json:"status"`
	TotalAmount        int64  `json:"total_amount"`
	CurrentPostVersion struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"current_post_version"`
	Placements []struct {
		ID               string   `json:"id"`
		Status           string   `json:"status"`
		StatusReason     string   `json:"status_reason"`
		AvailableActions []string `json:"available_actions"`
		History          []struct {
			Transition string `json:"transition"`
			ActorType  string `json:"actor_type"`
		} `json:"history"`
	} `json:"placements"`
}

type bloggerPlacementView struct {
	ID               string   `json:"id"`
	Status           string   `json:"status"`
	AvailableActions []string `json:"available_actions"`
	PublishAt        string   `json:"publish_at"`
	Post             *struct {
		FinalText string `json:"final_text"`
		Erid      string `json:"erid"`
	} `json:"post"`
}

// readyVersion двигает версию заказа так, как это сделают модерация и маркировка.
func (a *testApp) readyVersion(versionID string) {
	a.t.Helper()
	ctx := context.Background()
	id := uuid.MustParse(versionID)
	v, err := a.mods.post.Versions.Get(ctx, id)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.mods.post.ApproveVersion(ctx, id, v.CreatedBy); err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.mods.post.MarkVersionReady(ctx, id, "E-"+versionID[:8]); err != nil {
		a.t.Fatal(err)
	}
}

func TestPlacementOperationsHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-orders@example.ru")
	blg := a.bloggerClient("blg-orders@example.ru")
	ch := a.activeChannel(blg, "Рецепты", 1_000_000)
	campaign := a.campaignWithPost(adv, "Осень")

	// Кабинеты: блогеру корзина не доступна, рекламодателю — кабинет блогера.
	if r := blg.do(http.MethodGet, "/cart", nil); r.status != http.StatusForbidden {
		t.Fatalf("blogger cart: %d", r.status)
	}
	if r := adv.do(http.MethodGet, "/blogger/placements", nil); r.status != http.StatusForbidden {
		t.Fatalf("advertiser blogger placements: %d", r.status)
	}
	// CSRF обязателен для меняющих запросов.
	adv.csrf = false
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusForbidden {
		t.Fatalf("no csrf: %d", r.status)
	}
	adv.csrf = true

	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("add to cart: %d %s", r.status, r.body)
	}
	r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"})
	if _, details := r.errorCode(t); r.status != http.StatusConflict || details["reason"] != "already_in_cart" {
		t.Fatalf("duplicate: %d %s", r.status, r.body)
	}
	var catalog struct {
		InCart bool `json:"in_cart"`
	}
	adv.do(http.MethodGet, "/catalog/channels/"+ch, nil).json(t, &catalog)
	if !catalog.InCart {
		t.Fatal("in_cart in catalog card")
	}

	r = adv.doKey(http.MethodPost, "/campaigns/"+campaign+"/checkout", "checkout-1", nil)
	if r.status != http.StatusCreated {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	var order orderView
	r.json(t, &order)
	if order.Status != "moderation" || order.TotalAmount != 1_150_000 || len(order.Placements) != 1 ||
		order.Placements[0].History[0].Transition != "P-01" {
		t.Fatalf("order %+v", order)
	}
	var again orderView
	adv.doKey(http.MethodPost, "/campaigns/"+campaign+"/checkout", "checkout-1", nil).json(t, &again)
	if again.ID != order.ID {
		t.Fatal("repeated checkout created another order")
	}
	if r := adv.do(http.MethodGet, "/orders?limit=5", nil); r.status != http.StatusOK {
		t.Fatalf("list orders: %d", r.status)
	}

	// До P-02 блогер запроса не видит.
	var list struct {
		Items []bloggerPlacementView `json:"items"`
	}
	blg.do(http.MethodGet, "/blogger/placements", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatal("blogger sees placement before P-02")
	}
	a.readyVersion(order.CurrentPostVersion.ID)
	blg.do(http.MethodGet, "/blogger/placements?status=awaiting_blogger", nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Post == nil || list.Items[0].Post.Erid == "" {
		t.Fatalf("blogger list %+v", list)
	}
	pid := list.Items[0].ID
	if r := blg.doKey(http.MethodPost, "/blogger/placements/"+pid+"/accept", "acc-1", nil); r.status != http.StatusOK {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	r = blg.do(http.MethodPost, "/blogger/placements/"+pid+"/schedule", map[string]any{"mode": "at"})
	if code, details := r.errorCode(t); r.status != http.StatusUnprocessableEntity || code != "validation_failed" || details["reason"] != "publish_at_required" {
		t.Fatalf("schedule without publish_at: %d %s", r.status, r.body)
	}
	a.clk.Set(time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)) // 10:00 МСК
	var scheduled bloggerPlacementView
	r = blg.do(http.MethodPost, "/blogger/placements/"+pid+"/schedule", map[string]any{"mode": "at", "publish_at": "2026-10-20T09:00:00Z"})
	if r.status != http.StatusOK {
		t.Fatalf("schedule: %d %s", r.status, r.body)
	}
	r.json(t, &scheduled)
	if scheduled.Status != "scheduled" || scheduled.PublishAt != "2026-10-20T09:00:00Z" ||
		len(scheduled.AvailableActions) != 1 || scheduled.AvailableActions[0] != "reschedule" {
		t.Fatalf("scheduled %+v", scheduled)
	}
	r = blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil)
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "invalid_transition" {
		t.Fatalf("accept again: %d %s", r.status, r.body)
	}
	// Отмена рекламодателем после принятия — 409 invalid_transition.
	r = adv.do(http.MethodPost, "/placements/"+pid+"/cancel", nil)
	if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "invalid_transition" {
		t.Fatalf("cancel scheduled: %d %s", r.status, r.body)
	}
	// Приёмка и претензия до публикации — 409 invalid_transition (этап «Приёмка, споры и возвраты»).
	for _, path := range []string{"/accept", "/claims"} {
		r := adv.do(http.MethodPost, "/placements/"+pid+path, map[string]any{"reason": "other", "text": "Нет поста"})
		if code, _ := r.errorCode(t); r.status != http.StatusConflict || code != "invalid_transition" {
			t.Errorf("%s scheduled: %d %s", path, r.status, r.body)
		}
	}
}

func TestCheckoutInsufficientBalanceHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.newAdvertiser("adv-balance@example.ru")
	a.fund("adv-balance@example.ru", 150_000)
	blg := a.bloggerClient("blg-balance@example.ru")
	ch := a.activeChannel(blg, "Канал", 1_000_000)
	campaign := a.campaignWithPost(adv, "Кампания")
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("add to cart: %d", r.status)
	}
	r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil)
	code, details := r.errorCode(t)
	if r.status != http.StatusConflict || code != "insufficient_balance" || details["missing_amount"] != float64(1_000_000) {
		t.Fatalf("insufficient balance: %d %s", r.status, r.body)
	}
	// Корзина цела, заказа нет.
	var cart struct {
		Items []any `json:"items"`
	}
	adv.do(http.MethodGet, "/cart", nil).json(t, &cart)
	var orders struct {
		Items []any `json:"items"`
	}
	adv.do(http.MethodGet, "/orders", nil).json(t, &orders)
	if len(cart.Items) != 1 || len(orders.Items) != 0 {
		t.Fatalf("cart %d orders %d", len(cart.Items), len(orders.Items))
	}
	if a.count(`SELECT count(*) FROM hold`) != 0 || a.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'hold'`) != 0 {
		t.Fatal("rejected checkout wrote holds")
	}

	// Пополнили на нехватку: оформление удерживает всю сумму заказа (R-6).
	org := a.fund("adv-balance@example.ru", 1_000_000)
	if r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil); r.status != http.StatusCreated {
		t.Fatalf("checkout: %d %s", r.status, r.body)
	}
	available, held, err := a.mods.ledger.Balance(context.Background(), org)
	if err != nil || available != 0 || held != 1_150_000 {
		t.Fatalf("balance %d held %d: %v", available, held, err)
	}
	if a.count(`SELECT count(*) FROM hold WHERE status = 'active'`) != 1 {
		t.Fatal("hold is missing")
	}
}

// scheduledHTTP — размещение, запланированное через кабинеты на 20 октября 12:00 МСК.
func (a *testApp) scheduledHTTP(adv, blg *client, title string) (orderID, pid string) {
	a.t.Helper()
	ch := a.activeChannel(blg, title, 1_000_000)
	campaign := a.campaignWithPost(adv, "Кампания "+title)
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		a.t.Fatalf("add to cart: %d %s", r.status, r.body)
	}
	var order orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil).json(a.t, &order)
	a.readyVersion(order.CurrentPostVersion.ID)
	pid = order.Placements[0].ID
	if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
		a.t.Fatalf("accept: %d %s", r.status, r.body)
	}
	a.clk.Set(time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC))
	if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/schedule", map[string]any{"mode": "at", "publish_at": "2026-10-20T09:00:00Z"}); r.status != http.StatusOK {
		a.t.Fatalf("schedule: %d %s", r.status, r.body)
	}
	return order.ID, pid
}

type controlView struct {
	Placements []struct {
		Status         string `json:"status"`
		PublicationURL string `json:"publication_url"`
		Views          *int   `json:"views"`
		Dispute        *struct {
			Reason       string `json:"reason"`
			OpenedByType string `json:"opened_by_type"`
			DeadlineAt   string `json:"deadline_at"`
		} `json:"dispute"`
	} `json:"placements"`
}

func TestPublicationInCabinetsHTTP(t *testing.T) {
	a := newTestApp(t)
	adv := a.advertiserClient("adv-pub@example.ru")
	blg := a.bloggerClient("blg-pub@example.ru")
	orderID, pid := a.scheduledHTTP(adv, blg, "Рецепты")

	a.clk.Set(time.Date(2026, 10, 20, 9, 0, 3, 0, time.UTC))
	a.settle()
	posts := a.maxFake().Posts()
	if len(posts) != 1 {
		t.Fatalf("posts %d", len(posts))
	}
	a.maxFake().SetViews(posts[0].ID, 321)
	if err := a.mods.placement.CheckPublication(context.Background(), a.publicationID(pid), placementJobRef()); err != nil {
		t.Fatal(err)
	}
	var view controlView
	adv.do(http.MethodGet, "/orders/"+orderID, nil).json(t, &view)
	p := view.Placements[0]
	if p.Status != "published" || p.PublicationURL == "" || p.Views == nil || *p.Views != 321 || p.Dispute != nil {
		t.Fatalf("published view %+v", p)
	}
	var blgView struct {
		PublicationURL string `json:"publication_url"`
		Views          *int   `json:"views"`
	}
	blg.do(http.MethodGet, "/blogger/placements/"+pid, nil).json(t, &blgView)
	if blgView.PublicationURL != p.PublicationURL || blgView.Views == nil || *blgView.Views != 321 {
		t.Fatalf("blogger view %+v", blgView)
	}

	// Чужое удаление через Webhook — спор в обоих кабинетах, без предложенной доли.
	body := `{"update_type":"message_removed","timestamp":1792145000000,"message_id":"` + posts[0].ID + `","chat_id":1,"user_id":42}`
	if code := a.webhook(testWebhookPath, testWebhookSecret, body); code != http.StatusOK {
		t.Fatalf("webhook: %d", code)
	}
	a.settle()
	r := adv.do(http.MethodGet, "/orders/"+orderID, nil)
	r.json(t, &view)
	p = view.Placements[0]
	if p.Status != "disputed" || p.Dispute == nil || p.Dispute.Reason != "removed_early" || p.Dispute.OpenedByType != "system" {
		t.Fatalf("disputed view %+v", p)
	}
	if bytes.Contains(r.body, []byte("suggested_share")) {
		t.Fatal("suggested share shown to advertiser")
	}
}

// publicationID — публикация размещения.
func (a *testApp) publicationID(pid string) uuid.UUID {
	a.t.Helper()
	pub, err := a.mods.placementStore.Publications().GetByPlacement(context.Background(), uuid.MustParse(pid))
	if err != nil {
		a.t.Fatal(err)
	}
	return pub.ID
}

func placementJobRef() placementapp.JobRef { return placementapp.JobRef{ID: uuid.New()} }
