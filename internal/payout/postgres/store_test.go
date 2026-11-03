package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres"

	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

type env struct {
	d     *dbtest.DB
	store *postgres.Store
	tx    tx.Runner
}

func newEnv(t *testing.T) env {
	d := dbtest.New(t)
	log := logx.Discard()
	return env{d: d, store: postgres.New(d.Pool, log), tx: tx.NewRunner(d.Pool, log)}
}

// fixture — размещение самозанятого блогера и его исполнитель Рокет Ворк.
func (e env) fixture(t *testing.T) (ledgertest.Placement, domain.Contractor) {
	t.Helper()
	adv := ledgertest.Organization(t, e.d.Pool, accountdomain.SoleProprietor, "Рекламодатель")
	p := ledgertest.NewPlacement(t, e.d.Pool, adv, 1000000)
	c := domain.NewContractor(ids.New(), p.BloggerID, t0)
	got, created, err := e.store.InsertContractor(context.Background(), c)
	if err != nil || !created {
		t.Fatalf("insert contractor: %v %v", created, err)
	}
	return p, got
}

func (e env) payout(t *testing.T, p ledgertest.Placement, c domain.Contractor) domain.Payout {
	t.Helper()
	po, err := domain.NewPayout(ids.New(), p.ID, p.BloggerID, domain.MethodRocketwork, c.ID, p.BloggerAmount, t0)
	if err != nil {
		t.Fatal(err)
	}
	return po
}

func TestContractorInsertIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, c := e.fixture(t)
	again, created, err := e.store.InsertContractor(ctx, domain.NewContractor(ids.New(), p.BloggerID, t0))
	if err != nil || created || again.ID != c.ID {
		t.Fatalf("second insert: %v created=%v id=%s", err, created, again.ID)
	}
	c.Apply(domain.ProviderContractor{ExternalID: "rwc-1", Status: domain.ContractorActive}, t0)
	if err := e.store.UpdateContractor(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := e.store.ContractorByExternalID(ctx, "rwc-1", false)
	if err != nil || got.Status != domain.ContractorActive || got.StatusCheckedAt == nil {
		t.Fatalf("by external: %+v %v", got, err)
	}
	if _, err := e.store.ContractorByOrg(ctx, ids.New(), false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestPayoutConstraints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, c := e.fixture(t)
	first := e.payout(t, p, c)
	if err := e.store.InsertPayout(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Вторая живая выплата размещения не вставляется.
	if err := e.store.InsertPayout(ctx, e.payout(t, p, c)); !errors.Is(err, domain.ErrPayoutExists) {
		t.Fatalf("second live payout: %v", err)
	}
	// После failed — попытка 2.
	if _, err := first.MarkFailed(domain.FailureTaxStatusLost, "lost", t0); err != nil {
		t.Fatal(err)
	}
	if err := e.store.UpdatePayout(ctx, &first); err != nil {
		t.Fatal(err)
	}
	next, err := first.NextAttempt(ids.New(), c.ID, uuid.Nil, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertPayout(ctx, next); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	latest, err := e.store.LatestPayout(ctx, p.ID, false)
	if err != nil || latest.ID != next.ID || latest.Attempt != 2 {
		t.Fatalf("latest %+v %v", latest, err)
	}

	pool := e.d.Pool
	ins := `INSERT INTO payout (id, placement_id, blogger_organization_id, method, rocketwork_contractor_id, amount, attempt,
		status, failure_code, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 100, 9, $6, $7, $8, now(), now())`
	for name, args := range map[string][]any{
		"rocketwork without contractor": {"rocketwork", nil, "failed", "provider_error"},
		"bank with contractor":          {"bank_transfer", c.ID, "failed", "provider_error"},
		"failed without code":           {"rocketwork", c.ID, "failed", nil},
		"code without failed":           {"rocketwork", c.ID, "created", "provider_error"},
	} {
		id := ids.New()
		_, err := pool.Exec(ctx, ins, append([]any{id, p.ID, p.BloggerID}, append(args, id.String())...)...)
		if err == nil {
			t.Errorf("%s: inserted", name)
		}
	}
	// Роль приложения не удаляет выплаты и исполнителей.
	if _, err := pool.Exec(ctx, `DELETE FROM payout`); err == nil {
		t.Error("app role deleted payout")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM rocketwork_contractor`); err == nil {
		t.Error("app role deleted rocketwork_contractor")
	}
	// Основание проводки — существующая выплата.
	_, err = pool.Exec(ctx, `INSERT INTO ledger_transaction (id, kind, payout_id, actor_type, idempotency_key, entries_hash, created_at)
		VALUES ($1, 'payout', $2, 'system', 'k', 'h', now())`, ids.New(), ids.New())
	if err == nil || !strings.Contains(err.Error(), "ledger_transaction_payout_id_fkey") {
		t.Errorf("unknown payout_id: %v", err)
	}
}

func TestConcurrentUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, c := e.fixture(t)
	po := e.payout(t, p, c)
	if err := e.store.InsertPayout(ctx, po); err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp := po
			_, _ = cp.MarkSent("rw-"+string(rune('a'+i)), t0)
			err := e.store.UpdatePayout(ctx, &cp)
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrConcurrentUpdate):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestListSummaryAndYearSum(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adv := ledgertest.Organization(t, e.d.Pool, accountdomain.SoleProprietor, "Рекламодатель")
	first := ledgertest.NewPlacement(t, e.d.Pool, adv, 100000)
	c, _, err := e.store.InsertContractor(ctx, domain.NewContractor(ids.New(), first.BloggerID, t0))
	if err != nil {
		t.Fatal(err)
	}
	// Пять выплат одного блогера: размещения того же блогера (переносим организацию).
	var all []domain.Payout
	completed := []time.Time{
		time.Date(2026, 12, 31, 20, 59, 0, 0, time.UTC), // 31 декабря 23:59 МСК — 2026
		time.Date(2026, 12, 31, 21, 0, 0, 0, time.UTC),  // 1 января 00:00 МСК — 2027
	}
	for i := range 5 {
		pl := ledgertest.NewPlacement(t, e.d.Pool, adv, 100000)
		if _, err := e.d.Pool.Exec(ctx, `UPDATE placement SET blogger_organization_id = $1 WHERE id = $2`, first.BloggerID, pl.ID); err != nil {
			t.Fatal(err)
		}
		pl.BloggerID = first.BloggerID
		po := e.payout(t, pl, c)
		po.CreatedAt = t0.Add(time.Duration(i) * time.Minute)
		if i < 2 {
			_, _ = po.MarkSucceeded(100, "", "", completed[i])
		}
		if err := e.store.InsertPayout(ctx, po); err != nil {
			t.Fatal(err)
		}
		all = append(all, po)
	}
	var seen []uuid.UUID
	var after *domain.Cursor
	for {
		page, err := e.store.List(ctx, first.BloggerID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			seen = append(seen, p.ID)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.Cursor{At: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 5 || seen[0] != all[4].ID || seen[4] != all[0].ID {
		t.Fatalf("pages %v", seen)
	}
	sum, err := e.store.Summary(ctx, first.BloggerID)
	if err != nil || sum.Received != 200000 || sum.InProgress != 300000 || sum.Count != 5 {
		t.Fatalf("summary %+v %v", sum, err)
	}
	from, to := domain.YearBounds(completed[0])
	y2026, _ := e.store.SucceededSum(ctx, first.BloggerID, from, to)
	from, to = domain.YearBounds(completed[1])
	y2027, _ := e.store.SucceededSum(ctx, first.BloggerID, from, to)
	if y2026 != 100000 || y2027 != 100000 {
		t.Fatalf("year sums %d %d", y2026, y2027)
	}
	list, err := e.store.AdminList(ctx, domain.AdminFilter{Status: domain.StatusSucceeded})
	if err != nil || len(list) != 2 {
		t.Fatalf("admin list %d %v", len(list), err)
	}
}

func TestDepositReports(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adv := ledgertest.Organization(t, e.d.Pool, accountdomain.SoleProprietor, "Рекламодатель")
	self := ledgertest.NewPlacement(t, e.d.Pool, adv, 100000)
	legal := ledgertest.NewPlacement(t, e.d.Pool, adv, 50000)
	if _, err := e.d.Pool.Exec(ctx, `UPDATE organization SET legal_type = 'legal_entity', inn = '7707083893' WHERE id = $1`, legal.BloggerID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Pool.Exec(ctx, `UPDATE placement SET status = 'published' WHERE id = ANY($1)`, []uuid.UUID{self.ID, legal.ID}); err != nil {
		t.Fatal(err)
	}
	for _, org := range []uuid.UUID{self.BloggerID, legal.BloggerID} {
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO ledger_account (id, kind, organization_id, balance, created_at, updated_at)
			VALUES ($1, 'blogger_payable', $2, 7000, now(), now())`, ids.New(), org); err != nil {
			t.Fatal(err)
		}
	}
	payable, _ := e.store.RocketworkPayable(ctx)
	inFlight, _ := e.store.RocketworkInFlight(ctx)
	if payable != 7000 || inFlight != 100000 {
		t.Fatalf("payable %d in flight %d", payable, inFlight)
	}
	if _, err := e.d.Pool.Exec(ctx, `UPDATE placement SET status = 'accepted', status_changed_at = $2,
		performance_share = 10000, payout_amount = blogger_amount, fee_charged = fee_amount, refund_amount = 0 WHERE id = $1`,
		self.ID, t0.Add(-73*time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err := e.store.AcceptedUnpaid(ctx, t0.Add(-72*time.Hour))
	if err != nil || len(items) != 1 || items[0].ObjectKey != "placement:"+self.ID.String() {
		t.Fatalf("accepted unpaid %+v %v", items, err)
	}
}
