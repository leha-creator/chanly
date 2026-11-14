package app_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Rebuild the relevant services over the same database, as a deployment does.
func (e *env) feeRate(rate int) {
	channelDeps := e.channels.Deps
	channelDeps.Config.FeeRateBP = rate
	e.channels = channelapp.New(channelDeps)
	deps := e.svc.Deps
	deps.Config.FeeRateBP = rate
	deps.Channels = bridge.PlacementChannels{Svc: e.channels}
	e.svc = app.New(deps)
	e.post.SetVersionListener(bridge.VersionListener{Svc: e.svc})
	link := bridge.ChannelPlacements{Svc: e.svc}
	e.channels.SetPlacementPorts(link, link, link)
	e.channels.SetPostListener(bridge.ChannelPosts{Svc: e.svc})
}

func TestPlatformFeeCartCheckoutTransition(t *testing.T) {
	e := newEnv(t)
	_, _, oldChannel := e.blogger("Старый заказ", 1_000_000)
	e.addToCart(oldChannel)
	oldCampaign := e.campaign("Исторический заказ")
	oldOrder, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, oldCampaign, "fee-old-order")
	if err != nil {
		t.Fatal(err)
	}
	_, _, channel := e.blogger("Корзина до перехода", 1_000_000)
	e.addToCart(channel)
	before, _ := e.svc.GetCart(e.ctx(), e.adv)
	item := before.Lines[0].Item
	campaign := e.campaign("Новый заказ")
	e.feeRate(2500)
	for range 2 {
		cart, err := e.svc.GetCart(e.ctx(), e.adv)
		if err != nil || len(cart.Lines) != 1 || cart.Total != 1_250_000 || !cart.Lines[0].PriceChanged ||
			cart.Lines[0].Item.ShownTotal != 1_150_000 || !cart.Lines[0].Item.UpdatedAt.Equal(item.UpdatedAt) {
			t.Fatalf("unconfirmed cart: %+v %v", cart, err)
		}
	}
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "fee-new-order")
	wantReason(t, err, apperr.CodeCartChanged, "")
	if e.count(`SELECT count(*) FROM "order"`) != 1 || e.count(`SELECT count(*) FROM placement`) != 1 ||
		e.count(`SELECT count(*) FROM post_version`) != 1 {
		t.Fatal("changed cart created checkout records")
	}
	cart, err := e.svc.UpdateCartItem(e.ctx(), e.adv, item.ID, "f_1_24")
	if err != nil || cart.Lines[0].PriceChanged || cart.Lines[0].Item.ShownTotal != 1_250_000 {
		t.Fatalf("price confirmation: %+v %v", cart, err)
	}
	// Keep the old order's hold while leaving exactly the old price available.
	e.holds.SetBalance(e.adv, oldOrder.TotalAmount+1_150_000)
	_, err = e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "fee-new-order")
	wantReason(t, err, apperr.CodeInsufficientBalance, "")
	if ae, _ := apperr.As(err); ae.Details["missing_amount"] != int64(100_000) {
		t.Fatalf("missing amount: %+v", ae.Details)
	}
	if e.count(`SELECT count(*) FROM "order"`) != 1 || e.count(`SELECT count(*) FROM placement`) != 1 ||
		e.count(`SELECT count(*) FROM post_version`) != 1 {
		t.Fatal("insufficient balance left partial records")
	}
	e.holds.SetBalance(e.adv, 10_000_000)
	order, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, campaign, "fee-new-order")
	if err != nil || order.TotalAmount != 1_250_000 {
		t.Fatalf("new checkout: %+v %v", order, err)
	}
	p := e.getOrder(order.ID).Placements[0].Placement
	if p.FeeRateBP != 2500 || p.BloggerAmount != 1_000_000 || p.FeeAmount != 250_000 || !e.holds.Active(p.ID) {
		t.Fatalf("new snapshot/hold: %+v", p)
	}
	for _, tc := range []struct {
		campaign, order uuid.UUID
		key             string
	}{
		{oldCampaign, oldOrder.ID, "fee-old-order"}, {campaign, order.ID, "fee-new-order"},
	} {
		again, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, tc.campaign, tc.key)
		if err != nil || again.ID != tc.order {
			t.Fatalf("idempotent checkout: %+v %v", again, err)
		}
	}
	old := e.getOrder(oldOrder.ID).Placements[0].Placement
	if old.FeeRateBP != 1500 || old.TotalAmount != 1_150_000 || e.count(`SELECT count(*) FROM placement`) != 2 {
		t.Fatalf("historical order repriced: %+v", old)
	}
	if cart, err := e.svc.GetCart(e.ctx(), e.adv); err != nil || len(cart.Lines) != 0 {
		t.Fatalf("successful checkout cart: %+v %v", cart, err)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'cart.item_updated'`) != 1 {
		t.Fatal("cart read confirmed a price")
	}
}

func TestPlatformFeeNewPostVersionAndCancellationKeepSnapshot(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Старое размещение", 1_000_000)
	order := e.order(ch)
	pid := order.Placements[0].Placement.ID
	e.feeRate(2500)
	if _, err := e.post.RejectVersion(context.Background(), order.Version.ID, e.admin(), "Исправить пост"); err != nil {
		t.Fatal(err)
	}
	if e.holds.Active(pid) {
		t.Fatal("moderation rejection did not release hold")
	}
	updated, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, order.Order.ID, "fee-new-version")
	if err != nil {
		t.Fatal(err)
	}
	p := updated.Placements[0].Placement
	if p.ID != pid || p.FeeRateBP != 1500 || p.TotalAmount != 1_150_000 || p.FeeAmount != 150_000 || !e.holds.Active(pid) {
		t.Fatalf("new post version repriced placement: %+v", p)
	}
	for range 2 {
		if _, err := e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, pid, "fee-cancel"); err != nil {
			t.Fatal(err)
		}
	}
	p = e.placement(pid)
	if p.FeeRateBP != 1500 || p.TotalAmount != 1_150_000 || e.holds.Active(pid) || p.Status != domain.StatusCancelled {
		t.Fatalf("cancellation repriced placement: %+v", p)
	}
}

func TestPlatformFeeSettlementKeepsOriginalSnapshot(t *testing.T) {
	for _, rates := range [][2]int{{1500, 2500}, {2500, 1500}} {
		for _, share := range []int{0, 7500, 10000} {
			t.Run(fmt.Sprintf("%d_to_%d_share_%d", rates[0], rates[1], share), func(t *testing.T) {
				e := newEnv(t)
				e.feeRate(rates[0])
				pid := e.claimed(domain.ClaimOther)
				before := e.placement(pid)
				e.feeRate(rates[1])
				resolution := domain.ResolutionProportional
				if share == 0 {
					resolution = domain.ResolutionFullRefund
				}
				if share == 10000 {
					resolution = domain.ResolutionFullPayout
				}
				result, err := e.resolve(pid, resolution, &share)
				want, _ := domain.Distribute(before.BloggerAmount, before.FeeAmount, before.TotalAmount, share)
				if err != nil || result.Distribution != want {
					t.Fatalf("snapshot settlement: %+v want %+v, %v", result.Distribution, want, err)
				}
				after := e.placement(pid)
				if after.FeeRateBP != rates[0] || after.BloggerAmount != before.BloggerAmount || after.FeeAmount != before.FeeAmount ||
					after.TotalAmount != before.TotalAmount || *after.RefundAmount != want.RefundAmount {
					t.Fatalf("snapshot changed: %+v", after)
				}
			})
		}
	}
}

func TestPlatformFeeAcceptAfterConfigChange(t *testing.T) {
	e := newEnv(t)
	pid := e.removed()
	before := e.placement(pid)
	e.feeRate(2500)
	for range 2 {
		if _, err := e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "fee-accept"); err != nil {
			t.Fatal(err)
		}
	}
	after := e.placement(pid)
	if after.FeeRateBP != 1500 || *after.FeeCharged != before.FeeAmount || *after.PayoutAmount != before.BloggerAmount ||
		*after.RefundAmount != 0 || len(e.payouts.Created()) != 1 {
		t.Fatalf("historical acceptance: %+v", after)
	}
}
