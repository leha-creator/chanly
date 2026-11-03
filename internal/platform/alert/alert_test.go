package alert_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/mail/mailfake"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	d       *dbtest.DB
	clk     *clock.Manual
	alerter *alert.Alerter
	mail    *mailfake.Sender
	chat    *maxfake.Client
	worker  *job.Worker
}

func newEnv(t *testing.T, opts alert.Options) env {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	log := logx.Discard()
	e := env{d: d, clk: clk, mail: mailfake.New(), chat: maxfake.New(), worker: job.NewWorker(d.Pool, clk, log)}
	e.alerter = alert.New(d.Pool, tx.NewRunner(d.Pool, log), job.NewQueue(d.Pool, clk, log), clk, log, opts)
	e.alerter.NewSender(e.mail, e.chat).Register(e.worker)
	e.worker.SetAlerter(e.alerter)
	return e
}

var both = alert.Options{Emails: []string{"oncall@maxim.test"}, ChatID: -100500}

func (e env) sendJobs(t *testing.T, status string) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind = 'send_alert' AND status = $1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) run(t *testing.T) {
	t.Helper()
	if _, err := e.worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRaiseAtMostOncePerHour(t *testing.T) {
	e := newEnv(t, both)
	ctx := context.Background()
	raise := func() {
		if err := e.alerter.Raise(ctx, alert.Alert{Reason: "circuit_open:max", Summary: "Calls to max suspended",
			Fields: map[string]any{"failures": 10}}); err != nil {
			t.Fatal(err)
		}
	}
	raise()
	e.clk.Advance(10 * time.Minute)
	raise()
	if n := e.sendJobs(t, "pending"); n != 2 { // почта и чат
		t.Fatalf("%d send jobs, want 2", n)
	}
	var count int
	_ = e.d.Pool.QueryRow(ctx, `SELECT raised_count FROM alert`).Scan(&count)
	if count != 2 {
		t.Fatalf("raised_count %d", count)
	}
	e.run(t)
	if len(e.mail.Alerts()) != 1 || len(e.chat.Sent()) != 1 {
		t.Fatalf("mail %d chat %d", len(e.mail.Alerts()), len(e.chat.Sent()))
	}
	msg := e.chat.Sent()[0]
	if msg.ChatID != -100500 || !strings.Contains(msg.Text, "[ERROR] circuit_open:max") || !strings.Contains(msg.Text, "failures: 10") {
		t.Fatalf("chat message %+v", msg)
	}
	e.clk.Advance(51 * time.Minute)
	raise()
	if n := e.sendJobs(t, "pending"); n != 2 {
		t.Fatalf("after an hour: %d pending send jobs, want 2", n)
	}
}

func TestPartialFailureRetriesOnlyFailedChannel(t *testing.T) {
	e := newEnv(t, both)
	ctx := context.Background()
	e.mail.FailWith(errors.New("smtp down"))
	e.alerter.RaiseError(ctx, "downtime", "", "Worker was down", nil)
	e.run(t)
	if len(e.chat.Sent()) != 1 || e.sendJobs(t, "pending") != 1 {
		t.Fatalf("chat %d pending %d", len(e.chat.Sent()), e.sendJobs(t, "pending"))
	}
	e.mail.FailWith(nil)
	e.clk.Advance(time.Minute)
	e.run(t)
	if len(e.mail.Alerts()) != 1 || len(e.chat.Sent()) != 1 {
		t.Fatalf("after retry: mail %d chat %d", len(e.mail.Alerts()), len(e.chat.Sent()))
	}
}

func TestOnlyConfiguredChannels(t *testing.T) {
	e := newEnv(t, alert.Options{Emails: []string{"a@maxim.test", "b@maxim.test"}})
	e.alerter.RaiseWarn(context.Background(), "stuck:inbound_event", "evt-1", "Inbound event not processed", nil)
	if n := e.sendJobs(t, "pending"); n != 2 {
		t.Fatalf("%d send jobs, want 2 emails", n)
	}
	e.run(t)
	if got := e.mail.Alerts(); len(got) != 2 || got[0].Notice.Level != alert.LevelWarn {
		t.Fatalf("alerts %+v", got)
	}
}

func TestRaiseSurvivesCallerRollback(t *testing.T) {
	e := newEnv(t, both)
	ctx := context.Background()
	boom := errors.New("boom")
	err := tx.NewRunner(e.d.Pool, logx.Discard()).InTx(ctx, func(ctx context.Context) error {
		e.alerter.RaiseError(ctx, "invariant:I-01", "placement-1", "Invariant violated", nil)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n := e.sendJobs(t, "pending"); n != 2 {
		t.Fatalf("alert lost with caller rollback: %d jobs", n)
	}
}

func TestResolveAndOpenKeys(t *testing.T) {
	e := newEnv(t, both)
	ctx := context.Background()
	e.alerter.RaiseError(ctx, "stuck:inbound_event", "a", "s", nil)
	e.alerter.RaiseError(ctx, "stuck:inbound_event", "b", "s", nil)
	if err := e.alerter.Resolve(ctx, "stuck:inbound_event", "a"); err != nil {
		t.Fatal(err)
	}
	keys, err := e.alerter.OpenKeys(ctx, "stuck:inbound_event")
	if err != nil || len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("open keys %v err %v", keys, err)
	}
}

func TestExhaustedJobRaisesAlert(t *testing.T) {
	e := newEnv(t, both)
	ctx := context.Background()
	e.worker.Register("sync_ord_object", func(context.Context, job.Running) error { return errors.New("ord down") }, job.HandlerOptions{})
	if err := job.NewQueue(e.d.Pool, e.clk, logx.Discard()).Enqueue(ctx, job.Job{Kind: "sync_ord_object", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	var reason string
	if err := e.d.Pool.QueryRow(ctx, `SELECT reason FROM alert`).Scan(&reason); err != nil || reason != "job_failed:sync_ord_object" {
		t.Fatalf("reason %q err %v", reason, err)
	}
}
