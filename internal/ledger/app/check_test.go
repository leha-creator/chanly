package app_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// fakeAlerts — открытые оповещения «причина → объекты».
type fakeAlerts struct{ open map[string]map[string]bool }

func (f *fakeAlerts) Raise(_ context.Context, a alert.Alert) error {
	if f.open[a.Reason] == nil {
		f.open[a.Reason] = map[string]bool{}
	}
	f.open[a.Reason][a.ObjectKey] = true
	return nil
}

func (f *fakeAlerts) Resolve(_ context.Context, reason, objectKey string) error {
	delete(f.open[reason], objectKey)
	return nil
}

func (f *fakeAlerts) OpenKeys(_ context.Context, reason string) ([]string, error) {
	var out []string
	for k := range f.open[reason] {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeAlerts) isOpen(reason, key string) bool { return f.open[reason][key] }

func (f *fakeAlerts) total() int {
	n := 0
	for _, keys := range f.open {
		n += len(keys)
	}
	return n
}

// checkEnv — сервис с отчётами, оповещениями и метриками.
func checkEnv(t *testing.T) (*env, *fakeAlerts, *prometheus.Registry) {
	e := newEnv(t)
	alerts := &fakeAlerts{open: map[string]map[string]bool{}}
	reg := prometheus.NewRegistry()
	store := postgres.New(e.d.Pool, logx.Discard())
	reg.MustRegister(postgres.NewCollector(store))
	e.svc = app.New(app.Deps{Store: store, Reports: store, Tx: e.svc.Tx, Alerts: alerts, Metrics: postgres.NewMetrics(reg),
		Clock: e.clock, Log: logx.Discard()})
	return e, alerts, reg
}

func (e *env) check() app.CheckReport {
	e.t.Helper()
	r, err := e.svc.CheckBalance(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// owner выполняет запрос под ролью владельца схемы — порча журнала в обход прав приложения.
func (e *env) owner(query string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Owner.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestCheckBalanceClean(t *testing.T) {
	e, alerts, reg := checkEnv(t)
	e.deposit(e.org, 2_000_000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1_000_000)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	r := e.check()
	if !r.OK() || r.Assets != 2_000_000 || alerts.total() != 0 {
		t.Fatalf("report %+v, alerts %v", r, alerts.open)
	}
	if got := metric(t, reg, "maxim_ledger_transactions_total", "deposit"); got != 1 {
		t.Fatalf("deposit transactions metric = %v", got)
	}
	for kind, want := range map[string]float64{"bank": 2_000_000, "advertiser_balance": float64(2_000_000 - p.TotalAmount),
		"placement_hold": float64(p.TotalAmount)} {
		if got := metric(t, reg, "maxim_ledger_balance_kopecks", kind); got != want {
			t.Fatalf("balance metric %s = %v, want %v", kind, got, want)
		}
	}
}

func TestCheckBalanceCacheMismatch(t *testing.T) {
	e, alerts, reg := checkEnv(t)
	e.deposit(e.org, 2_000_000)
	var accountID string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT id::text FROM ledger_account WHERE kind = 'advertiser_balance'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	e.owner(`UPDATE ledger_account SET balance = balance + 100 WHERE id = $1`, accountID)
	r := e.check()
	if r.OK() || len(r.CacheMismatches) != 1 || r.Liabilities-r.Assets != 100 {
		t.Fatalf("report %+v", r)
	}
	if !alerts.isOpen(app.ReasonCacheMismatch, accountID) || !alerts.isOpen(app.ReasonImbalance, "") {
		t.Fatalf("alerts %v", alerts.open)
	}
	if got := metric(t, reg, "maxim_ledger_check_failures_total", app.CheckCache); got != 1 {
		t.Fatalf("cache failures metric = %v", got)
	}
	e.owner(`UPDATE ledger_account SET balance = balance - 100 WHERE id = $1`, accountID)
	if r := e.check(); !r.OK() || alerts.total() != 0 {
		t.Fatalf("after repair: %+v, alerts %v", r, alerts.open)
	}
}

func TestCheckBalanceHoldMismatch(t *testing.T) {
	e, alerts, _ := checkEnv(t)
	e.deposit(e.org, 2_000_000)
	p := ledgertest.NewPlacement(t, e.d.Pool, e.org, 1_000_000)
	if err := e.inTx(func(ctx context.Context) error { return e.svc.HoldAll(ctx, e.org, []app.HoldRequest{request(p)}) }); err != nil {
		t.Fatal(err)
	}
	if err := e.inTx(func(ctx context.Context) error { return e.svc.Release(ctx, p.ID, "cancelled") }); err != nil {
		t.Fatal(err)
	}
	// Удержание снова активно, а остаток placement_hold — ноль.
	e.owner(`UPDATE hold SET status = 'active', closed_at = NULL WHERE placement_id = $1`, p.ID)
	r := e.check()
	if len(r.HoldMismatches) != 1 || r.HoldMismatches[0].Held != p.TotalAmount || r.HoldMismatches[0].Balance != 0 {
		t.Fatalf("report %+v", r)
	}
	if !alerts.isOpen(app.ReasonHoldMismatch, p.ID.String()) || alerts.total() != 1 {
		t.Fatalf("alerts %v", alerts.open)
	}
	e.owner(`UPDATE hold SET status = 'released', closed_at = now() WHERE placement_id = $1`, p.ID)
	if r := e.check(); !r.OK() || alerts.total() != 0 {
		t.Fatalf("after repair: %+v, alerts %v", r, alerts.open)
	}
}

// metric — значение счётчика или показателя с единственной меткой label; нет — 0.
func metric(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() != label {
				continue
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}
