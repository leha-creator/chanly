package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	billing "github.com/leha-creator/chanly/internal/billing/domain"
	marking "github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/metrics"
)

// Restart the composition over the existing test database, sessions and fake integrations.
func (a *testApp) restartFee(rate int) {
	a.t.Helper()
	cfg := a.mods.cfg
	cfg.Channel.FeeRateBP = rate
	in := a.mods.in
	m := newModules(cfg, in)
	if err := m.startMarking(context.Background()); err != nil {
		a.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.t.Cleanup(cancel)
	h, err := apiHandler(ctx, cfg, in, m, metrics.New())
	if err != nil {
		a.t.Fatal(err)
	}
	a.srv.Close()
	srv := httptest.NewServer(h)
	a.t.Cleanup(func() { cancel(); srv.Close() })
	a.srv = srv
	a.mods = m
	a.worker = newWorker(in, m)
}

func TestPlatformFeeHTTPDeploymentAndRollback(t *testing.T) {
	for _, share := range []int{10000, 7500} {
		t.Run(strconv.Itoa(share), func(t *testing.T) { testPlatformFeeHTTPTransition(t, share) })
	}
}

func testPlatformFeeHTTPTransition(t *testing.T, share int) {
	a := newTestApp(t) // Existing harness deliberately represents the historical 1500 period.
	adv := a.advertiserClient("fee-transition@example.ru")
	blg := a.bloggerClient("fee-blogger@example.ru")
	old := a.orderOn(adv, a.activeChannel(blg, "Старый тариф", 1_000_000), "Старый заказ")
	ch := a.activeChannel(blg, "Новый тариф", 1_000_000)
	if r := adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"}); r.status != http.StatusCreated {
		t.Fatalf("cart: %d %s", r.status, r.body)
	}
	campaign := a.campaignWithPost(adv, "После перехода")
	defaults, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil || defaults.Channel.FeeRateBP != 2500 {
		t.Fatalf("current default: %d %v", defaults.Channel.FeeRateBP, err)
	}
	a.restartFee(defaults.Channel.FeeRateBP)
	var card struct {
		Prices []struct {
			Total   int64 `json:"total_amount"`
			Blogger int64 `json:"amount"`
		} `json:"prices"`
	}
	adv.do(http.MethodGet, "/catalog/channels/"+ch, nil).json(t, &card)
	if len(card.Prices) != 1 || card.Prices[0].Total != 1_250_000 {
		t.Fatalf("new catalog: %+v", card)
	}
	blg.do(http.MethodGet, "/blogger/channels/"+ch, nil).json(t, &card)
	if len(card.Prices) != 1 || card.Prices[0].Blogger != 1_000_000 || card.Prices[0].Total != 1_250_000 {
		t.Fatalf("blogger prices after cutover: %+v", card)
	}
	var cart struct {
		Items []struct {
			ID      string `json:"id"`
			Shown   int64  `json:"shown_total_amount"`
			Current int64  `json:"current_total_amount"`
			Changed bool   `json:"price_changed"`
		} `json:"items"`
	}
	adv.do(http.MethodGet, "/cart", nil).json(t, &cart)
	if len(cart.Items) != 1 || cart.Items[0].Shown != 1_150_000 || cart.Items[0].Current != 1_250_000 || !cart.Items[0].Changed {
		t.Fatalf("changed cart: %+v", cart)
	}
	if r := adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil); r.status != http.StatusConflict {
		t.Fatalf("unconfirmed: %d %s", r.status, r.body)
	}
	if r := adv.do(http.MethodPatch, "/cart/items/"+cart.Items[0].ID, map[string]any{"format_id": "f_1_24"}); r.status != http.StatusOK {
		t.Fatalf("confirm: %d %s", r.status, r.body)
	}
	var current orderView
	adv.doKey(http.MethodPost, "/campaigns/"+campaign+"/checkout", "fee-new", nil).json(t, &current)
	if old.TotalAmount != 1_150_000 || current.TotalAmount != 1_250_000 {
		t.Fatalf("orders: old=%d new=%d", old.TotalAmount, current.TotalAmount)
	}
	a.readyVersion(old.CurrentPostVersion.ID)
	a.readyVersion(current.CurrentPostVersion.ID)
	a.clk.Set(time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC))
	for _, o := range []orderView{old, current} {
		pid := o.Placements[0].ID
		if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/accept", nil); r.status != http.StatusOK {
			t.Fatalf("accept request: %d %s", r.status, r.body)
		}
		if r := blg.do(http.MethodPost, "/blogger/placements/"+pid+"/schedule", map[string]any{"mode": "at", "publish_at": "2026-10-20T09:00:00Z"}); r.status != http.StatusOK {
			t.Fatalf("schedule: %d %s", r.status, r.body)
		}
	}
	a.clk.Set(time.Date(2026, 10, 20, 9, 0, 3, 0, time.UTC))
	a.settle()
	a.clk.Set(time.Date(2026, 10, 21, 9, 0, 5, 0, time.UTC))
	a.settle()
	if _, err := a.db.Pool.Exec(context.Background(), "UPDATE publication SET views=100; UPDATE placement SET final_views=100"); err != nil {
		t.Fatal(err)
	}
	period, _ := marking.ParsePeriod("2026-10")
	rows, err := a.mods.marking.Source.Placements(context.Background(), period)
	if err != nil || len(rows) != 2 {
		t.Fatalf("report source: %+v %v", rows, err)
	}
	for _, r := range rows {
		want := int64(150_000)
		if r.PlacementID.String() == current.Placements[0].ID {
			want = 250_000
		}
		if r.CommissionAmount != want || r.BloggerAmount != 1_000_000 || r.AdvertiserAmount != 1_000_000+want {
			t.Fatalf("ORD snapshot: %+v", r)
		}
	}
	oldPID, newPID := old.Placements[0].ID, current.Placements[0].ID
	if r := adv.doKey(http.MethodPost, "/placements/"+oldPID+"/accept", "old-accept", nil); r.status != http.StatusOK {
		t.Fatalf("old acceptance: %d %s", r.status, r.body)
	}
	a.restartFee(1500) // A rollback must not reprice the new 25% placement.
	if share == 10000 {
		for range 2 {
			if r := adv.doKey(http.MethodPost, "/placements/"+newPID+"/accept", "new-accept", nil); r.status != http.StatusOK {
				t.Fatalf("new acceptance after rollback: %d %s", r.status, r.body)
			}
		}
	} else if _, err := a.mods.placement.DevAccept(context.Background(), uuidOf(t, newPID), share); err != nil {
		t.Fatal(err)
	}
	rows, err = a.mods.marking.Source.Placements(context.Background(), period)
	if err != nil || len(rows) != 2 {
		t.Fatalf("settled report source: %+v %v", rows, err)
	}
	for _, r := range rows {
		wantFee, wantBlogger := int64(150_000), int64(1_000_000)
		if r.PlacementID.String() == newPID {
			wantFee = 250_000 * int64(share) / 10000
			wantBlogger = 1_000_000 * int64(share) / 10000
		}
		if r.CommissionAmount != wantFee || r.BloggerAmount != wantBlogger || r.CommissionAmount != r.AdvertiserAmount-r.BloggerAmount {
			t.Fatalf("ORD settlement: %+v", r)
		}
	}
	a.ledgerOK("fee transition and rollback")
	p, _ := billing.ParsePeriod("2026-10")
	a.clk.Set(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC))
	res, err := a.mods.billing.GenerateClosingActs(context.Background(), p)
	if err != nil || res.Created != 1 {
		t.Fatalf("closing: %+v %v", res, err)
	}
	acts, err := a.mods.billing.ListClosingActs(context.Background(), adv.orgID(t), "", 10)
	if err != nil || len(acts.Items) != 1 || int64(acts.Items[0].FeeAmount) != 150_000+250_000*int64(share)/10000 || int64(acts.Items[0].PlacementsAmount) != 1_000_000+1_000_000*int64(share)/10000 {
		t.Fatalf("mixed closing act: %+v %v", acts, err)
	}
	again, err := a.mods.billing.GenerateClosingActs(context.Background(), p)
	if err != nil || again.Created != 0 || again.Skipped != 1 {
		t.Fatalf("closing replay: %+v %v", again, err)
	}
}
