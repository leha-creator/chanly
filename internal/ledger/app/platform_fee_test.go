package app_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	placement "github.com/leha-creator/chanly/internal/placement/domain"
)

func TestPlatformFeeMixedSnapshotsLedger(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 5_000_000)
	for _, rate := range []int{1500, 2500} {
		p := ledgertest.NewPlacementWithFee(t, e.d.Pool, e.org, 1_000_000, rate)
		d, err := placement.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, 7500)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := e.inTx(func(ctx context.Context) error {
				return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)})
			}); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := e.inTx(func(ctx context.Context) error {
				return e.svc.Settle(ctx, p.ID, p.BloggerID, domain.Distribution{
					Payout: d.PayoutAmount, FeeCharged: d.FeeCharged, Refund: d.RefundAmount,
				}, domain.ActorSystem, uuid.Nil)
			}); err != nil {
				t.Fatal(err)
			}
		}
		if e.balance(domain.PlacementHold(p.ID)) != 0 || e.balance(domain.BloggerPayable(p.BloggerID)) != 750_000 {
			t.Fatalf("rate %d: settlement balances", rate)
		}
	}
	if e.balance(domain.PlatformRevenue()) != 300_000 || e.balance(domain.AdvertiserBalance(e.org)) != 3_200_000 ||
		e.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'settlement'`) != 2 {
		t.Fatal("mixed snapshot settlements were repriced or duplicated")
	}
	// A new 25% hold is released at its actual original amount, including on replay.
	p := ledgertest.NewPlacementWithFee(t, e.d.Pool, e.org, 1_000_000, 2500)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.inTx(func(ctx context.Context) error { return e.svc.Release(ctx, p.ID, "fee-cancel") }); err != nil {
			t.Fatal(err)
		}
	}
	if e.balance(domain.PlacementHold(p.ID)) != 0 || e.balance(domain.AdvertiserBalance(e.org)) != 3_200_000 {
		t.Fatal("release changed original hold amount")
	}
	e.converges()
}
