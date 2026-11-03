package stuck_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/stuck"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	d       *dbtest.DB
	clk     *clock.Manual
	alerter *alert.Alerter
	checker *stuck.Checker
}

func newEnv(t *testing.T) env {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	log := logx.Discard()
	a := alert.New(d.Pool, tx.NewRunner(d.Pool, log), job.NewQueue(d.Pool, clk, log), clk, log, alert.Options{Emails: []string{"oncall@maxim.test"}})
	return env{d: d, clk: clk, alerter: a, checker: stuck.New(a, clk, log)}
}

func (e env) openAlerts(t *testing.T, reason string) []string {
	t.Helper()
	keys, err := e.alerter.OpenKeys(context.Background(), reason)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func (e env) sendJobs(t *testing.T) int {
	t.Helper()
	var n int
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind = 'send_alert'`).Scan(&n)
	return n
}

func TestStaleInboundEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.checker.Add(stuck.InboundEvents(e.d.Pool, 10*time.Minute))
	id := ids.New()
	if _, err := e.d.Pool.Exec(ctx, `INSERT INTO inbound_event (id, source, external_key, event_type, payload, status, created_at, updated_at)
		VALUES ($1, 'max', 'k', 'bot_added', '{}', 'received', $2, $2)`, id, start); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(9 * time.Minute)
	_ = e.checker.Run(ctx, job.Running{})
	if got := e.openAlerts(t, "stuck:inbound_event"); len(got) != 0 {
		t.Fatalf("alert before threshold: %v", got)
	}
	e.clk.Advance(2 * time.Minute)
	_ = e.checker.Run(ctx, job.Running{})
	if got := e.openAlerts(t, "stuck:inbound_event"); len(got) != 1 || got[0] != id.String() {
		t.Fatalf("open alerts %v", got)
	}
	// Через 2 часа объект всё ещё завис — повторного письма нет.
	e.clk.Advance(2 * time.Hour)
	_ = e.checker.Run(ctx, job.Running{})
	if n := e.sendJobs(t); n != 1 {
		t.Fatalf("%d send jobs, want 1", n)
	}
	// Событие обработано — оповещение закрыто.
	if _, err := e.d.Pool.Exec(ctx, `UPDATE inbound_event SET status = 'processed'`); err != nil {
		t.Fatal(err)
	}
	_ = e.checker.Run(ctx, job.Running{})
	if got := e.openAlerts(t, "stuck:inbound_event"); len(got) != 0 {
		t.Fatalf("alert not resolved: %v", got)
	}
}

func TestFailedJobWithoutAlert(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.checker.Add(stuck.FailedJobs(e.d.Pool))
	withAlert, without := ids.New(), ids.New()
	for _, id := range []any{withAlert, without} {
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO job (id, kind, payload, run_at, status, max_attempts, finished_at, created_at, updated_at)
			VALUES ($1, 'x', '{}', $2, 'failed', 1, $2, $2, $2)`, id, start); err != nil {
			t.Fatal(err)
		}
	}
	e.alerter.RaiseError(ctx, "job_failed:x", withAlert.String(), "s", nil)
	e.clk.Advance(time.Second)
	_ = e.checker.Run(ctx, job.Running{})
	if got := e.openAlerts(t, "stuck:failed_job"); len(got) != 1 || got[0] != without.String() {
		t.Fatalf("open alerts %v", got)
	}
}

func TestFailingCheckDoesNotStopOthers(t *testing.T) {
	e := newEnv(t)
	e.checker.Add(stuck.Check{Name: "broken", Find: func(context.Context, time.Time) ([]string, error) { return nil, errors.New("boom") }})
	e.checker.Add(stuck.Check{Name: "ok", Find: func(context.Context, time.Time) ([]string, error) { return []string{"obj"}, nil }})
	if err := e.checker.Run(context.Background(), job.Running{}); err == nil {
		t.Fatal("error of the broken check is lost")
	}
	if got := e.openAlerts(t, "stuck:ok"); len(got) != 1 {
		t.Fatalf("second check did not run: %v", got)
	}
}
