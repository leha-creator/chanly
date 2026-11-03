package app_test

import (
	"context"
	"io"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/pdf"
	"github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// platformAccount — расчётный счёт платформы в тестах (ключ верный для БИК 044525225).
const platformAccount = "40802810838000000001"

var start = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

type env struct {
	t       *testing.T
	d       *dbtest.DB
	clk     *clock.Manual
	svc     *app.Service
	ledger  *ledgerapp.Service
	storage *storagefake.Storage
	alerts  *fakeAlerts
	org     uuid.UUID
	user    uuid.UUID
	admin   uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	log := logx.Discard()
	clk := clock.NewManual(start)
	runner := tx.NewRunner(d.Pool, log)
	ledgerStore := ledgerpg.New(d.Pool, log)
	ledger := ledgerapp.New(ledgerapp.Deps{Store: ledgerStore, Reports: ledgerStore, Tx: runner, Clock: clk, Log: log})
	store := postgres.New(d.Pool, log)
	auditLog := audit.New(d.Pool, clk, log)
	files := storagefake.New()
	alerts := &fakeAlerts{open: map[string]bool{}}
	svc := app.New(app.Deps{
		Invoices: store.Invoices(), Statements: store.Statements(), BankTransactions: store.BankTransactions(),
		ClosingActs: store.ClosingActs(), Ledger: bridge.BillingLedger{Svc: ledger},
		Organizations: bridge.BillingOrganizations{Orgs: accountpg.New(d.Pool, log).Organizations()},
		Renderer:      pdf.New(clk, log), Parser: clientbank.New(0, log), Storage: files,
		Alerts: alerts, Limits: auditLog, Tx: runner, Audit: auditLog, Clock: clk, Log: log,
		Config: app.Config{
			InvoiceTTL: 336 * time.Hour, InvoicesPerUserHour: 10, InvoicesPerIPHour: 30, VATNote: "Без НДС",
			Supplier: domain.Supplier{Name: "ИП Платформа", INN: "500100732259", BankAccount: platformAccount,
				BankName: "ПАО СБЕРБАНК", BankBIK: "044525225", BankCorrAccount: "30101810400000000225"},
		},
	})
	return &env{
		t: t, d: d, clk: clk, svc: svc, ledger: ledger, storage: files, alerts: alerts,
		org:   ledgertest.Organization(t, d.Pool, accountdomain.LegalEntity, "Рекламодатель"),
		user:  ledgertest.User(t, d.Pool),
		admin: ledgertest.User(t, d.Pool),
	}
}

// as — context пользователя кабинета рекламодателя с адресом ip (как кладёт слой HTTP).
func (e *env) as(user uuid.UUID, ip string) context.Context {
	addr := netip.MustParseAddr(ip)
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, UserID: &user, OrganizationID: &e.org, IP: &addr})
}

func (e *env) invoice(amount money.Kopecks) domain.Invoice {
	e.t.Helper()
	ip := netip.MustParseAddr("192.0.2.1")
	inv, err := e.svc.CreateInvoice(e.as(e.user, "192.0.2.1"), e.org, e.user, amount, &ip)
	if err != nil {
		e.t.Fatalf("create invoice: %v", err)
	}
	return inv
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) balance(org uuid.UUID) money.Kopecks {
	e.t.Helper()
	available, _, err := e.ledger.Balance(context.Background(), org)
	if err != nil {
		e.t.Fatal(err)
	}
	return available
}

func (e *env) storedPDF(key string) []byte {
	e.t.Helper()
	r, _, err := e.storage.Get(context.Background(), key)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Close()
	data, _ := io.ReadAll(r)
	return data
}

// converges — сумма активов равна сумме пассивов и кеш равен проводкам (R-15).
func (e *env) converges() {
	e.t.Helper()
	store := ledgerpg.New(e.d.Pool, logx.Discard())
	assets, liabilities, err := store.SidesTotal(context.Background())
	if err != nil || assets != liabilities {
		e.t.Fatalf("assets %d liabilities %d: %v", assets, liabilities, err)
	}
	if m, err := store.CacheMismatches(context.Background()); err != nil || len(m) != 0 {
		e.t.Fatalf("cache mismatches %v: %v", m, err)
	}
}

// fakeAlerts — открытые оповещения «причина:объект».
type fakeAlerts struct{ open map[string]bool }

func (f *fakeAlerts) Raise(_ context.Context, a alert.Alert) error {
	f.open[a.Reason+":"+a.ObjectKey] = true
	return nil
}

func (f *fakeAlerts) Resolve(_ context.Context, reason, objectKey string) error {
	delete(f.open, reason+":"+objectKey)
	return nil
}
