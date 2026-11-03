package app_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

type env struct {
	t          *testing.T
	d          *dbtest.DB
	clk        *clock.Manual
	tx         tx.Runner
	svc        *app.Service
	ledger     *ledgerapp.Service
	rw         *rocketworkfake.Fake
	placements *fakePlacements
	notices    *fakeNotifier
	alerts     *fakeAlerts
	admin      uuid.UUID
	advertiser uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	log := logx.Discard()
	clk := clock.NewManual(start)
	runner := tx.NewRunner(d.Pool, log)
	ledgerStore := ledgerpg.New(d.Pool, log)
	ledger := ledgerapp.New(ledgerapp.Deps{Store: ledgerStore, Reports: ledgerStore, Tx: runner, Clock: clk, Log: log})
	rw := rocketworkfake.New([]byte("secret"))
	e := &env{
		t: t, d: d, clk: clk, tx: runner, ledger: ledger, rw: rw,
		placements: &fakePlacements{info: map[uuid.UUID]domain.PlacementInfo{}},
		notices:    &fakeNotifier{}, alerts: &fakeAlerts{open: map[string]bool{}},
		admin:      ledgertest.User(t, d.Pool),
		advertiser: ledgertest.Organization(t, d.Pool, accountdomain.LegalEntity, "Рекламодатель"),
	}
	e.svc = app.New(app.Deps{
		Store: postgres.New(d.Pool, log), Provider: bridge.PayoutProvider{API: rw, Log: log},
		Ledger: bridge.PayoutLedger{Svc: ledger}, Placements: e.placements,
		Organizations: bridge.PayoutOrganizations{Orgs: accountpg.New(d.Pool, log).Organizations()},
		Notifier:      e.notices, Alerts: e.alerts, Tx: runner, Jobs: job.NewQueue(d.Pool, clk, log),
		Audit: audit.New(d.Pool, clk, log), Clock: clk, Log: log,
		Config: app.Config{ProviderEnabled: true, NPDAnnualLimit: 100000, DepositTolerance: 100},
	})
	return e
}

// blogger — организация блогера с исполнителем Рокет Ворк в статусе active (кроме юрлица).
func (e *env) blogger(legal accountdomain.LegalType) uuid.UUID {
	e.t.Helper()
	org := ledgertest.Organization(e.t, e.d.Pool, legal, "Блогер")
	if legal == accountdomain.LegalEntity {
		return org
	}
	ctx := context.Background()
	if _, err := e.svc.Invite(ctx, org); err != nil {
		e.t.Fatalf("invite: %v", err)
	}
	if err := e.svc.EnsureContractor(ctx, org); err != nil && !isPostponed(err) {
		e.t.Fatalf("ensure: %v", err)
	}
	ext := e.contractor(org).ExternalID
	if err := e.rw.ActivateContractor(ext); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.ApplyContractor(ctx, ext); err != nil {
		e.t.Fatalf("apply contractor: %v", err)
	}
	return org
}

func (e *env) contractor(org uuid.UUID) domain.Contractor {
	e.t.Helper()
	c, err := postgres.New(e.d.Pool, logx.Discard()).ContractorByOrg(context.Background(), org, false)
	if err != nil {
		e.t.Fatalf("contractor: %v", err)
	}
	return c
}

// accepted — размещение блогера org, принятое с выплатой amount: начисление blogger_payable
// ручной проводкой и выплата, созданная как при переходе в accepted.
func (e *env) accepted(org uuid.UUID, amount money.Kopecks) (placementID, payoutID uuid.UUID) {
	e.t.Helper()
	p := ledgertest.NewPlacement(e.t, e.d.Pool, e.advertiser, amount)
	e.placements.set(domain.PlacementInfo{ID: p.ID, Status: domain.PlacementStatusAccepted, BloggerOrgID: org, PayoutAmount: amount})
	err := e.tx.InTx(context.Background(), func(ctx context.Context) error {
		if _, err := e.ledger.Post(ctx, ledgerdomain.NewManual("payable:"+p.ID.String(),
			[]ledgerdomain.Posting{{Debit: ledgerdomain.Bank(), Credit: ledgerdomain.BloggerPayable(org), Amount: amount}},
			e.admin, "начисление в тесте")); err != nil {
			return err
		}
		var err error
		payoutID, err = e.svc.Create(ctx, app.PayoutRequest{PlacementID: p.ID, BloggerOrgID: org, Amount: amount})
		return err
	})
	if err != nil {
		e.t.Fatalf("accepted: %v", err)
	}
	return p.ID, payoutID
}

func (e *env) payout(id uuid.UUID) domain.Payout {
	e.t.Helper()
	p, err := e.svc.Payout(context.Background(), id)
	if err != nil {
		e.t.Fatalf("payout: %v", err)
	}
	return p
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) jobs(dedupeKey string) int {
	return e.count(`SELECT count(*) FROM job WHERE dedupe_key = $1`, dedupeKey)
}

func (e *env) payable(org uuid.UUID) money.Kopecks {
	e.t.Helper()
	var v int64
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT coalesce(sum(balance), 0) FROM ledger_account
		WHERE kind = 'blogger_payable' AND organization_id = $1`, org).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return money.Kopecks(v)
}

func isPostponed(err error) bool {
	_, ok := job.PostponedFor(err)
	return ok
}

// fakePlacements — размещения в памяти: Info по заданным данным, MarkPaid меняет статус.
type fakePlacements struct {
	mu       sync.Mutex
	info     map[uuid.UUID]domain.PlacementInfo
	paid     []uuid.UUID
	failPaid error
}

func (f *fakePlacements) set(i domain.PlacementInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info[i.ID] = i
}

func (f *fakePlacements) MarkPaid(_ context.Context, placementID, _ uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPaid != nil {
		return f.failPaid
	}
	i := f.info[placementID]
	if i.Status != domain.PlacementStatusAccepted {
		return domain.ErrPlacementState
	}
	i.Status = "paid"
	f.info[placementID] = i
	f.paid = append(f.paid, placementID)
	return nil
}

func (f *fakePlacements) Info(_ context.Context, id uuid.UUID) (domain.PlacementInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.info[id]
	if !ok {
		return domain.PlacementInfo{}, domain.ErrNotFound
	}
	return i, nil
}

func (f *fakePlacements) Brief(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.PlacementBrief, error) {
	out := map[uuid.UUID]domain.PlacementBrief{}
	for _, id := range ids {
		out[id] = domain.PlacementBrief{OrderNumber: 7, ChannelTitle: "Канал блогера"}
	}
	return out, nil
}

func (f *fakePlacements) paidCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paid)
}

type fakeNotifier struct {
	mu        sync.Mutex
	templates []string
}

func (n *fakeNotifier) Notify(_ context.Context, x domain.Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.templates = append(n.templates, x.Template)
	return nil
}

func (n *fakeNotifier) count(template string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, t := range n.templates {
		if t == template {
			c++
		}
	}
	return c
}

type fakeAlerts struct {
	mu   sync.Mutex
	open map[string]bool
}

func (f *fakeAlerts) Raise(_ context.Context, a alert.Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open[a.Reason+":"+a.ObjectKey] = true
	return nil
}

func (f *fakeAlerts) Resolve(_ context.Context, reason, objectKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.open, reason+":"+objectKey)
	return nil
}

func (f *fakeAlerts) isOpen(reason, objectKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open[reason+":"+objectKey]
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}
