package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

func TestCheckout(t *testing.T) {
	e := newEnv(t)
	blogger1, _, ch1 := e.blogger("Первый", 1_000_000)
	ch2 := placementtest.Channel(t, e.d.Pool, blogger1, "Второй", 2_000_000)
	_, _, ch3 := e.blogger("Третий", 1_000_001)
	for _, ch := range []uuid.UUID{ch1, ch2, ch3} {
		e.addToCart(ch)
	}
	campaign := e.campaign("Осень")

	o, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "checkout-key")
	if err != nil {
		t.Fatal(err)
	}
	view := e.getOrder(o.ID)
	if o.Number != 1001 || view.Order.Status != domain.OrderModeration || view.Version.Number != 1 ||
		view.Version.Status != domain.VersionPendingModeration || view.CampaignName != "Осень" {
		t.Fatalf("order %+v version %+v", view.Order, view.Version)
	}
	if !strings.Contains(view.Version.Body, "utm_campaign=order_1001") {
		t.Fatalf("version body without order UTM: %q", view.Version.Body)
	}
	// 06 §3: 11 500,00 + 23 000,00 + (10 000,01 + 1 500,00).
	if view.Order.TotalAmount != 1_150_000+2_300_000+1_150_001 || len(view.Placements) != 3 || view.Counts[domain.StatusCreated] != 3 {
		t.Fatalf("total %d placements %d", view.Order.TotalAmount, len(view.Placements))
	}
	w := domain.NewWindow(postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22), 9*60, 21*60)
	for _, pv := range view.Placements {
		p := pv.Placement
		if p.Status != domain.StatusCreated || !p.WindowStart.Equal(w.Start) || !p.WindowEnd.Equal(w.End) || p.TimeFrom != w.TimeFrom || p.PostVersionID != view.Version.ID || p.FeeRateBP != 1500 ||
			p.TotalAmount != p.BloggerAmount+p.FeeAmount || p.SentToBloggerAt != nil {
			t.Fatalf("placement %+v", p)
		}
		if len(pv.History) != 1 || pv.History[0].Transition != domain.P01Create || pv.History[0].ActorType != domain.ActorAdvertiser ||
			*pv.History[0].ActorUserID != e.advUser {
			t.Fatalf("history %+v", pv.History)
		}
		if got := e.jobRunAt(domain.TimerKey(p.ID, domain.TimerWindow)); !got.Equal(w.End.Add(-domain.DefaultTiming.WindowGuard)) {
			t.Fatalf("T-14 at %s", got)
		}
		if !e.holds.Active(p.ID) {
			t.Fatalf("no hold for %s", p.ID)
		}
		if strings.Join(pv.Actions, ",") != domain.ActionCancel {
			t.Fatalf("actions %v", pv.Actions)
		}
	}
	if cart, _ := e.svc.GetCart(context.Background(), e.adv); len(cart.Lines) != 0 {
		t.Fatal("cart not cleared")
	}
	c, _ := e.post.GetCampaign(context.Background(), e.adv, campaign)
	if c.Status != postdomain.CampaignOrdered || c.OrderID == nil || *c.OrderID != o.ID {
		t.Fatalf("campaign %+v", c)
	}
	if e.holds.OutsideTx != 0 {
		t.Fatal("holds called outside transaction")
	}

	// R-18: повтор с тем же ключом — тот же заказ, второго нет.
	again, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "checkout-key")
	if err != nil || again.ID != o.ID {
		t.Fatalf("repeat: %v %v", again.ID, err)
	}
	if n := e.count(`SELECT count(*) FROM "order"`); n != 1 {
		t.Fatalf("orders %d", n)
	}
	// Без ключа кампания уже оформлена — 409.
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
	wantReason(t, err, apperr.CodeConflict, "campaign_not_draft")

	if len(e.logged("order created")) != 1 || len(e.logged("idempotent repeat")) != 1 {
		t.Fatal("checkout log events")
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'order.created'`); n != 1 {
		t.Fatalf("audit %d", n)
	}
}

func TestCheckoutRejects(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)

	// Нет дат кампании.
	name := "Без дат"
	bare, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	e.addToCart(ch)
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, bare.ID, "")
	wantReason(t, err, apperr.CodeCampaignIncomplete, "")
	if ae, _ := apperr.As(err); len(ae.Details["missing_fields"].([]string)) != 4 {
		t.Fatalf("missing fields %v", ae.Details)
	}

	// Пост с проблемой: пустой черновик.
	from, to := postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22)
	tf, tt := postdomain.TimeOfDay(9*60), postdomain.TimeOfDay(21*60)
	name = "Пустой пост"
	empty, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{Name: &name, DateFrom: &from,
		DateTo: &to, TimeFrom: &tf, TimeTo: &tt})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, empty.ID, "")
	wantReason(t, err, apperr.CodeCampaignIncomplete, "")
	if ae, _ := apperr.As(err); len(ae.Details["post_problems"].([]string)) == 0 {
		t.Fatalf("post problems %v", ae.Details)
	}

	// Цена изменилась после добавления — cart_changed, ничего не создано.
	campaign := e.campaign("Кампания")
	placementtest.SetPrice(t, e.d.Pool, ch, 1_100_000)
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
	wantReason(t, err, apperr.CodeCartChanged, "")
	if ae, _ := apperr.As(err); len(ae.Details["items"].([]string)) != 1 {
		t.Fatalf("items %v", ae.Details)
	}
	if n := e.count(`SELECT count(*) FROM "order"`) + e.count(`SELECT count(*) FROM placement`) +
		e.count(`SELECT count(*) FROM post_version`); n != 0 {
		t.Fatalf("records after rejected checkout: %d", n)
	}

	// Не хватает баланса — insufficient_balance с недостающей суммой, ничего не создано.
	cart, _ := e.svc.GetCart(context.Background(), e.adv)
	if _, err := e.svc.UpdateCartItem(e.ctx(), e.adv, cart.Lines[0].Item.ID, "f_1_24"); err != nil {
		t.Fatal(err)
	}
	e.holds.SetBalance(e.adv, 1_000_000)
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
	wantReason(t, err, apperr.CodeInsufficientBalance, "")
	if ae, _ := apperr.As(err); ae.Details["missing_amount"] != int64(1_265_000-1_000_000) {
		t.Fatalf("missing amount %v", ae.Details)
	}
	if n := e.count(`SELECT count(*) FROM "order"`) + e.count(`SELECT count(*) FROM placement`); n != 0 {
		t.Fatalf("records after insufficient balance: %d", n)
	}
	if cart, _ := e.svc.GetCart(context.Background(), e.adv); len(cart.Lines) != 1 {
		t.Fatal("cart cleared after rejected checkout")
	}
	c, _ := e.post.GetCampaign(context.Background(), e.adv, campaign)
	if c.Status != postdomain.CampaignDraft {
		t.Fatalf("campaign %s", c.Status)
	}
	if len(e.logged("checkout rejected")) != 4 {
		t.Fatalf("checkout rejected logged %d", len(e.logged("checkout rejected")))
	}

	// Пустая корзина — cart_changed.
	e.holds.SetBalance(e.adv, 100_000_000)
	if _, err := e.svc.DeleteCartItem(e.ctx(), e.adv, cart.Lines[0].Item.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
	wantReason(t, err, apperr.CodeCartChanged, "")

	// Чужая кампания — 404.
	stranger, strangerUser := placementtest.Blogger(t, e.d.Pool)
	if _, err := e.svc.Checkout(e.ctx(), stranger, strangerUser, campaign, ""); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign campaign: %v", err)
	}
}

func TestParallelCheckoutOneOrder(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	e.addToCart(ch)
	campaign := e.campaign("Кампания")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !apperr.Is(err, apperr.CodeConflict) {
			t.Fatalf("parallel: %v", err)
		}
	}
	if ok != 1 || e.count(`SELECT count(*) FROM "order"`) != 1 {
		t.Fatalf("succeeded %d, orders %d", ok, e.count(`SELECT count(*) FROM "order"`))
	}
}

func TestListOrders(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	var created []uuid.UUID
	for range 3 {
		created = append(created, e.order(ch).Order.ID)
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for {
		page, err := e.svc.ListOrders(context.Background(), e.adv, nil, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			seen[it.Order.ID] = true
			if it.Counts[domain.StatusCreated] != 1 || it.CampaignName != "Кампания" {
				t.Fatalf("summary %+v", it)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("listed %d of %v", len(seen), created)
	}
	st := domain.OrderCancelled
	if page, _ := e.svc.ListOrders(context.Background(), e.adv, &st, "", 10); len(page.Items) != 0 {
		t.Fatal("status filter")
	}
	stranger, _ := placementtest.Blogger(t, e.d.Pool)
	if _, err := e.svc.GetOrder(context.Background(), stranger, created[0]); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign order: %v", err)
	}
	if _, err := e.svc.ListOrders(context.Background(), e.adv, nil, "garbage!", 10); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("broken cursor: %v", err)
	}
}
