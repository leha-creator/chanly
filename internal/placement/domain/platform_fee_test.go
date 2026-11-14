package domain

import (
	"testing"

	"github.com/google/uuid"

	channel "github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func TestPlatformFeePriceParityAndSnapshot(t *testing.T) {
	for _, tc := range []struct {
		amount, fee, total money.Kopecks
		rate               int
	}{
		{10_000, 1_500, 11_500, 1500},
		{10_007, 1_501, 11_508, 1500},
		{1_000_001, 150_000, 1_150_001, 1500},
		{1_000_000_000, 150_000_000, 1_150_000_000, 1500},
		{100, 25, 125, 2500}, // Existing development price floor.
		{10_000, 2_500, 12_500, 2500},
		{10_007, 2_501, 12_508, 2500},
		{1_000_000, 250_000, 1_250_000, 2500},
		{1_000_001, 250_000, 1_250_001, 2500},
		{1_000_000_000, 250_000_000, 1_250_000_000, 2500},
	} {
		offer := testOffer(uuid.New())
		price := offer.Prices[0]
		price.Amount = tc.amount
		p, _, err := NewPlacement(uuid.New(), uuid.New(), offer, price, testWindow(), uuid.New(), tc.rate,
			uuid.New(), mskTime(2026, 10, 20, 8, 0), DefaultTiming)
		if err != nil {
			t.Fatal(err)
		}
		if tc.amount.ShareBP(tc.rate) != tc.fee || TotalAmount(tc.amount, tc.rate) != tc.total ||
			channel.TotalAmount(tc.amount, tc.rate) != tc.total || p.FeeRateBP != tc.rate ||
			p.BloggerAmount != tc.amount || p.FeeAmount != tc.fee || p.TotalAmount != tc.total {
			t.Fatalf("amount=%d rate=%d: snapshot=%+v, want fee=%d total=%d", tc.amount, tc.rate, p, tc.fee, tc.total)
		}
	}
}

func TestPlatformFeeDistribution(t *testing.T) {
	for _, tc := range []struct {
		fee                   money.Kopecks
		share                 int
		payout, charged, back money.Kopecks
	}{
		{150_000, 7500, 750_000, 112_500, 287_500},
		{250_000, 0, 0, 0, 1_250_001},
		{250_000, 1, 100, 25, 1_249_876},
		{250_000, 3333, 333_300, 83_325, 833_376},
		{250_000, 7500, 750_000, 187_500, 312_501},
		{250_000, 9999, 999_900, 249_975, 126},
		{250_000, 10000, 1_000_001, 250_000, 0},
	} {
		blogger := money.Kopecks(1_000_001)
		if tc.fee == 150_000 {
			blogger = 1_000_000
		}
		d, err := Distribute(blogger, tc.fee, blogger+tc.fee, tc.share)
		if err != nil || d.PayoutAmount != tc.payout || d.FeeCharged != tc.charged || d.RefundAmount != tc.back ||
			d.PayoutAmount+d.FeeCharged+d.RefundAmount != blogger+tc.fee {
			t.Fatalf("fee=%d share=%d: %+v, %v", tc.fee, tc.share, d, err)
		}
	}
	// Each placement rounds its own commission before order totals are summed.
	if got := OrderTotal([]Placement{{TotalAmount: TotalAmount(10_007, 2500)}, {TotalAmount: TotalAmount(10_007, 2500)}}); got != 25_016 {
		t.Fatalf("per-placement rounding: %d", got)
	}
}

func TestCartFeeChangeComparesAmounts(t *testing.T) {
	offer := testOffer(uuid.New())
	item := CartItem{ID: uuid.New(), ChannelID: offer.Ref.ID, FormatID: "f_1_24", ShownTotal: 1_150_000}
	lines := EvaluateCart([]CartItem{item}, map[uuid.UUID]ChannelOffer{offer.Ref.ID: offer}, 2500)
	if !lines[0].PriceChanged || *lines[0].Current != 1_250_000 || lines[0].Item.ShownTotal != 1_150_000 || CheckCartForCheckout(lines) == nil {
		t.Fatalf("unconfirmed fee change: %+v", lines[0])
	}
	// The flag compares totals, even when different rates round to the same amount.
	offer.Prices[0].Amount = 1
	item.ShownTotal = 1
	lines = EvaluateCart([]CartItem{item}, map[uuid.UUID]ChannelOffer{offer.Ref.ID: offer}, 2500)
	if lines[0].PriceChanged || *lines[0].Current != 1 {
		t.Fatalf("equal rounded amounts: %+v", lines[0])
	}
}
