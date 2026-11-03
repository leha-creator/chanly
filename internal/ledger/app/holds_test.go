package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// inTx выполняет fn в транзакции базы.
func (e *env) inTx(fn func(ctx context.Context) error) error {
	return e.svc.Tx.InTx(context.Background(), fn)
}

func (e *env) holdStatuses(placementID uuid.UUID) []string {
	e.t.Helper()
	rows, err := e.d.Pool.Query(context.Background(), `SELECT status FROM hold WHERE placement_id = $1 ORDER BY created_at, id`, placementID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func request(p ledgertest.Placement) app.HoldRequest {
	return app.HoldRequest{PlacementID: p.ID, PostVersionID: p.VersionID, Amount: p.TotalAmount}
}

// Оформление: удержание и проводка на каждое размещение, свободный остаток уменьшился на сумму
// заказа; повтор — без новых проводок (R-6).
func TestHoldAll(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 3000000)
	p1 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	p2 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 200000)
	reqs := []app.HoldRequest{request(p1), request(p2)}
	for range 2 {
		if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, reqs) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.balance(domain.AdvertiserBalance(e.org)); got != 3000000-p1.TotalAmount-p2.TotalAmount {
		t.Fatalf("available = %d", got)
	}
	if n := e.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'hold'`); n != 2 {
		t.Fatalf("hold transactions = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM hold WHERE status = 'active'`); n != 2 {
		t.Fatalf("active holds = %d", n)
	}
	available, held, err := e.svc.Balance(context.Background(), e.org)
	if err != nil || held != p1.TotalAmount+p2.TotalAmount || available != 3000000-held {
		t.Fatalf("balance %d/%d, %v", available, held, err)
	}
	var active map[uuid.UUID]bool
	_ = e.inTx(func(ctx context.Context) error {
		active, err = e.svc.HasActive(ctx, []uuid.UUID{p1.ID, p2.ID, ids.New()})
		return err
	})
	if len(active) != 3 || !active[p1.ID] || !active[p2.ID] {
		t.Fatalf("HasActive = %v", active)
	}
	e.converges()
}

// Нехватка: общая нехватка по всем размещениям, ничего не записано (R-6).
func TestHoldAllInsufficient(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 1200000)
	p1 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	p2 := ledgertest.NewPlacement(t, e.d.Pool, e.org, 200000)
	err := e.inTx(func(ctx context.Context) error {
		return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p1), request(p2)})
	})
	var neg *domain.NegativeLiabilityError
	if !errors.As(err, &neg) || neg.Missing != p1.TotalAmount+p2.TotalAmount-1200000 {
		t.Fatalf("insufficient: %v", err)
	}
	if e.count(`SELECT count(*) FROM hold`) != 0 || e.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'hold'`) != 0 {
		t.Fatal("rejected holds were written")
	}
	if e.balance(domain.AdvertiserBalance(e.org)) != 1200000 {
		t.Fatal("balance changed")
	}
}

// Снятие и новое удержание с ключом новой версии (S-29); снятие без удержания — no-op.
func TestReleaseAndNewVersion(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 2000000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.inTx(func(ctx context.Context) error { return e.svc.Release(ctx, p.ID, "post_rejected") }); err != nil {
			t.Fatal(err)
		}
	}
	if e.balance(domain.AdvertiserBalance(e.org)) != 2000000 || e.balance(domain.PlacementHold(p.ID)) != 0 {
		t.Fatal("release did not return the hold")
	}
	if n := e.count(`SELECT count(*) FROM ledger_transaction WHERE kind = 'hold_release'`); n != 1 {
		t.Fatalf("hold_release transactions = %d", n)
	}
	next := ids.New()
	if _, err := e.d.Pool.Exec(context.Background(), `INSERT INTO post_version (id, post_id, number, status, content_doc, body,
		token_carrier, token_position, advertiser_name, advertiser_inn, created_by, created_at)
		SELECT $1, post_id, 2, status, content_doc, body, token_carrier, token_position, advertiser_name, advertiser_inn,
		created_by, now() FROM post_version WHERE id = $2`, next, p.VersionID); err != nil {
		t.Fatal(err)
	}
	if err := e.inTx(func(ctx context.Context) error {
		return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{{PlacementID: p.ID, PostVersionID: next, Amount: p.TotalAmount}})
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.holdStatuses(p.ID); len(got) != 2 || got[0] != "released" || got[1] != "active" {
		t.Fatalf("holds = %v", got)
	}
	if n := e.count(`SELECT count(*) FROM ledger_transaction WHERE idempotency_key = $1`, domain.HoldKey(p.ID, next)); n != 1 {
		t.Fatal("new version hold is missing")
	}
	// Повтор удержания уже снятой версии — нарушение правила, откат.
	err := e.inTx(func(ctx context.Context) error {
		if err := e.svc.Release(ctx, p.ID, "cancelled"); err != nil {
			return err
		}
		return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{{PlacementID: p.ID, PostVersionID: next, Amount: p.TotalAmount}})
	})
	if !errors.Is(err, domain.ErrInvariant) {
		t.Fatalf("hold replay after release: %v", err)
	}
	e.converges()
}

// Распределение с долей 75 %: три проводки, остаток удержания 0, distributed; повтор — без изменений.
func TestSettle(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 2000000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	d := domain.Distribution{Payout: 750000, FeeCharged: 112500, Refund: 287500}
	for range 2 {
		if err := e.inTx(func(ctx context.Context) error {
			return e.svc.Settle(ctx, p.ID, p.BloggerID, d, domain.ActorSystem, uuid.Nil)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT count(*) FROM ledger_entry e JOIN ledger_transaction t ON t.id = e.ledger_transaction_id WHERE t.kind = 'settlement'`); n != 3 {
		t.Fatalf("settlement entries = %d", n)
	}
	if e.balance(domain.PlacementHold(p.ID)) != 0 || e.balance(domain.BloggerPayable(p.BloggerID)) != 750000 ||
		e.balance(domain.PlatformRevenue()) != 112500 || e.balance(domain.AdvertiserBalance(e.org)) != 2000000-p.TotalAmount+287500 {
		t.Fatal("settlement balances")
	}
	if got := e.holdStatuses(p.ID); len(got) != 1 || got[0] != "distributed" {
		t.Fatalf("holds = %v", got)
	}
	e.converges()
}

// Сумма частей не равна удержанию — ERROR и откат, удержание остаётся активным (I-12).
func TestSettleMismatch(t *testing.T) {
	e := newEnv(t)
	e.deposit(e.org, 2000000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000000)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	err := e.inTx(func(ctx context.Context) error {
		return e.svc.Settle(ctx, p.ID, p.BloggerID, domain.Distribution{Payout: 1000000}, domain.ActorSystem, uuid.Nil)
	})
	if !errors.Is(err, domain.ErrInvariant) {
		t.Fatalf("mismatch: %v", err)
	}
	if got := e.holdStatuses(p.ID); len(got) != 1 || got[0] != "active" {
		t.Fatalf("holds = %v", got)
	}
	// Без удержания и без прежнего распределения — тоже нарушение.
	other := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1000)
	err = e.inTx(func(ctx context.Context) error {
		return e.svc.Settle(ctx, other.ID, other.BloggerID, domain.Distribution{Payout: 1000}, domain.ActorSystem, uuid.Nil)
	})
	if !errors.Is(err, domain.ErrInvariant) {
		t.Fatalf("settle without hold: %v", err)
	}
}

func TestHoldsRequireTransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.HoldAll(ctx, e.org, nil); !errors.Is(err, domain.ErrNoTransaction) {
		t.Fatalf("HoldAll: %v", err)
	}
	if err := e.svc.Release(ctx, ids.New(), "x"); !errors.Is(err, domain.ErrNoTransaction) {
		t.Fatalf("Release: %v", err)
	}
	if err := e.svc.Settle(ctx, ids.New(), ids.New(), domain.Distribution{}, domain.ActorSystem, uuid.Nil); !errors.Is(err, domain.ErrNoTransaction) {
		t.Fatalf("Settle: %v", err)
	}
}
