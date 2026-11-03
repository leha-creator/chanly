package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Каждая строка 06 §2.3 через Post: остатки и сходимость после неё (07 §9).
func TestEveryKindConverges(t *testing.T) {
	e := newEnv(t)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	p2 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 200000)
	payout := ledgertest.Payout(t, e.d.Pool, p, 750000)

	steps := []struct {
		tx    domain.Transaction
		check map[domain.AccountRef]money.Kopecks
	}{
		{domain.NewDeposit(e.bankTransaction(e.org, 2000000), uuid.Nil, e.org, 2000000, domain.ActorSystem, uuid.Nil),
			map[domain.AccountRef]money.Kopecks{domain.Bank(): 2000000, domain.AdvertiserBalance(e.org): 2000000}},
		{domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount),
			map[domain.AccountRef]money.Kopecks{domain.AdvertiserBalance(e.org): 850000, domain.PlacementHold(p.ID): 1150000}},
		{domain.NewHold(p2.ID, p2.VersionID, e.org, p2.TotalAmount),
			map[domain.AccountRef]money.Kopecks{domain.AdvertiserBalance(e.org): 620000}},
		{domain.NewHoldRelease(p2.ID, p2.VersionID, e.org, p2.TotalAmount),
			map[domain.AccountRef]money.Kopecks{domain.AdvertiserBalance(e.org): 850000, domain.PlacementHold(p2.ID): 0}},
		{domain.NewSettlement(p.ID, e.org, p.BloggerID, domain.Distribution{Payout: 750000, FeeCharged: 112500, Refund: 287500}, domain.ActorSystem, uuid.Nil),
			map[domain.AccountRef]money.Kopecks{domain.PlacementHold(p.ID): 0, domain.BloggerPayable(p.BloggerID): 750000,
				domain.PlatformRevenue(): 112500, domain.AdvertiserBalance(e.org): 1137500}},
		{domain.NewDepositTopup("t1", 1000000, e.admin, "перевод на депозит"),
			map[domain.AccountRef]money.Kopecks{domain.Bank(): 1000000, domain.RocketworkDeposit(): 1000000}},
		{domain.NewPayout(payout, p.ID, p.BloggerID, 750000, 7500),
			map[domain.AccountRef]money.Kopecks{domain.BloggerPayable(p.BloggerID): 0, domain.RocketworkDeposit(): 242500, domain.PayoutFees(): 7500}},
		{domain.NewBalanceReturn("r1", e.org, 137500, e.admin, "обращение 15"),
			map[domain.AccountRef]money.Kopecks{domain.AdvertiserBalance(e.org): 1000000, domain.Bank(): 862500}},
		{domain.NewManual("m1", []domain.Posting{{Debit: domain.PlatformRevenue(), Credit: domain.AdvertiserBalance(e.org), Amount: 12500}}, e.admin, "компенсация"),
			map[domain.AccountRef]money.Kopecks{domain.PlatformRevenue(): 100000, domain.AdvertiserBalance(e.org): 1012500}},
	}
	for _, s := range steps {
		res := e.mustPost(s.tx)
		if res.Replayed || res.ID == uuid.Nil {
			t.Fatalf("%s: result %+v", s.tx.Kind, res)
		}
		for ref, want := range s.check {
			if got := e.balance(ref); got != want {
				t.Errorf("after %s: %s = %d, want %d", s.tx.Kind, ref.Kind, got, want)
			}
		}
		e.converges()
	}
	var entries int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entry`).Scan(&entries); err != nil || entries != 12 {
		t.Fatalf("entries = %d, %v; want 12", entries, err)
	}
}

func TestIdempotency(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 2000000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)

	first := e.mustPost(domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount))
	again := e.mustPost(domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount))
	if !again.Replayed || again.ID != first.ID {
		t.Fatalf("replay = %+v, first %+v", again, first)
	}
	if e.balance(domain.AdvertiserBalance(e.org)) != 850000 {
		t.Fatal("replay changed the balance")
	}
	// Тот же ключ с другой суммой — отказ, ничего не записано.
	if _, err := e.post(domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount+1)); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_transaction WHERE kind = 'hold'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hold transactions = %d, %v", n, err)
	}
	e.converges()
}

func TestInsufficientBalance(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 1000000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	_, err := e.post(domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount))
	var neg *domain.NegativeLiabilityError
	if !errors.As(err, &neg) || neg.Missing != 150000 {
		t.Fatalf("hold above balance: %v", err)
	}
	if e.balance(domain.AdvertiserBalance(e.org)) != 1000000 || e.balance(domain.PlacementHold(p.ID)) != 0 {
		t.Fatal("rejected hold changed balances")
	}
	e.converges()
}

// Два одновременных удержания на весь баланс: проходит одно (блокировка счёта, R-5).
func TestConcurrentHolds(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 1150000)
	p1 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	p2 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, p := range []ledgertest.Placement{p1, p2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.post(domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		var neg *domain.NegativeLiabilityError
		switch {
		case err == nil:
			ok++
		case !errors.As(err, &neg):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || e.balance(domain.AdvertiserBalance(e.org)) != 0 {
		t.Fatalf("successful holds = %d, balance %d", ok, e.balance(domain.AdvertiserBalance(e.org)))
	}
	e.converges()
}

func TestPostRequiresTransaction(t *testing.T) {
	e := newEnv(t)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	if _, err := e.svc.Post(context.Background(), domain.NewHold(p.ID, p.VersionID, e.org, p.TotalAmount)); !errors.Is(err, domain.ErrNoTransaction) {
		t.Fatalf("Post without transaction: %v", err)
	}
}

// Откат транзакции вызывающего откатывает проводку (I-10).
func TestCallerRollbackRevertsPosting(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("caller failed")
	err := e.svc.Tx.InTx(context.Background(), func(ctx context.Context) error {
		bankTx := e.bankTransaction(e.org, 100000)
		if _, err := e.svc.Post(ctx, domain.NewDeposit(bankTx, uuid.Nil, e.org, 100000, domain.ActorSystem, uuid.Nil)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if e.balance(domain.AdvertiserBalance(e.org)) != 0 {
		t.Fatal("posting survived rollback")
	}
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_transaction`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transactions after rollback = %d, %v", n, err)
	}
}

func TestInvalidPostingIsRejected(t *testing.T) {
	e := newEnv(t)
	bad := domain.NewManual("x", []domain.Posting{{Debit: domain.Bank(), Credit: domain.AdvertiserBalance(e.org), Amount: 1}}, e.admin, "")
	if _, err := e.post(bad); !errors.Is(err, domain.ErrInvalidPosting) {
		t.Fatalf("manual without reason: %v", err)
	}
}
