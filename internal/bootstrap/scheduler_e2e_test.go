package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/mail/mailfake"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

const (
	oncallEmail  = "oncall@maxim.test"
	oncallChatID = -100500
	botToken     = "max-bot-secret-token"
)

// schedulerApp — собранный worker этапа «Планировщик заданий и интеграционный
// слой»: настоящая PostgreSQL, поддельные почта и MAX, часы с ручным ходом.
type schedulerApp struct {
	t      *testing.T
	db     *dbtest.DB
	clk    *clock.Manual
	in     *infra
	mods   *modules
	worker *job.Worker
	mail   *mailfake.Sender
	chat   *maxfake.Client
	logs   *bytes.Buffer
}

func newSchedulerApp(t *testing.T) *schedulerApp {
	t.Helper()
	d := dbtest.New(t)
	logs := &bytes.Buffer{}
	log := logx.New(&lockedWriter{w: logs}, slog.LevelDebug, "worker")
	clk := clock.NewManual(testStart)
	cfg := testConfig()
	cfg.Alert = AlertConfig{Emails: []string{oncallEmail}, MAXChatID: oncallChatID}
	a := &schedulerApp{t: t, db: d, clk: clk, mail: mailfake.New(), chat: maxfake.New(), logs: logs}
	a.in = &infra{pool: d.Pool, clock: clk, log: log, mail: a.mail, max: a.chat}
	a.in.alerter = newAlerter(cfg, a.in)
	a.mods = newModules(cfg, a.in)
	a.worker = newWorker(a.in, a.mods)
	return a
}

// drain выполняет готовые задания, пока они есть.
func (a *schedulerApp) drain() {
	a.t.Helper()
	for range 20 {
		n, err := a.worker.RunOnce(context.Background())
		if err != nil {
			a.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	a.t.Fatal("jobs keep coming after 20 rounds")
}

func (a *schedulerApp) count(sql string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func (a *schedulerApp) queue() *job.Queue { return job.NewQueue(a.db.Pool, a.clk, a.in.log) }

func TestSchedulerAfterDowntime(t *testing.T) {
	a := newSchedulerApp(t)
	ctx := context.Background()
	var ranNow, expired atomic.Int32
	a.worker.Register("test_remove", func(context.Context, job.Running) error { ranNow.Add(1); return nil },
		job.HandlerOptions{Priority: job.PriorityHigh, ObjectRef: func(p []byte) string {
			var v struct {
				PlacementID string `json:"placement_id"`
			}
			_ = json.Unmarshal(p, &v)
			return "placement:" + v.PlacementID
		}})
	a.worker.Register("test_publish", func(context.Context, job.Running) error { return nil },
		job.HandlerOptions{Priority: job.PriorityHigh, Overdue: job.Overdue{After: 30 * time.Minute,
			OnExpired: func(context.Context, job.Running) error { expired.Add(1); return nil }}})

	// Worker работал до testStart, затем 45 минут простоя.
	if err := a.worker.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.worker.EnsurePeriodic(ctx); err != nil {
		t.Fatal(err)
	}
	_ = a.queue().Enqueue(ctx,
		job.Job{Kind: "test_remove", MaxAttempts: job.Unlimited, RunAt: testStart.Add(time.Minute), Payload: map[string]string{"placement_id": "p-1"}},
		job.Job{Kind: "test_publish", MaxAttempts: job.Unlimited, RunAt: testStart.Add(time.Minute)})
	a.clk.Advance(3 * time.Hour)

	gap, err := a.worker.CheckDowntime(ctx)
	if err != nil || gap != 3*time.Hour {
		t.Fatalf("gap %v err %v", gap, err)
	}
	a.drain()
	if ranNow.Load() != 1 || expired.Load() != 1 {
		t.Fatalf("remove ran %d, publish expired %d", ranNow.Load(), expired.Load())
	}
	// Периодическое задание после простоя в 3 интервала выполнено один раз.
	if n := a.count(`SELECT count(*) FROM job WHERE kind = 'check_stuck_objects' AND status = 'succeeded'`); n != 1 {
		t.Fatalf("check_stuck_objects ran %d times", n)
	}
	if n := a.count(`SELECT count(*) FROM job WHERE kind = 'check_stuck_objects' AND status = 'pending'`); n != 1 {
		t.Fatalf("next check_stuck_objects not scheduled: %d", n)
	}
	// Оповещение о простое: письмо и сообщение в чат дежурных.
	mails := a.mail.Alerts()
	if len(mails) != 1 || mails[0].To != oncallEmail || mails[0].Notice.Reason != "downtime" {
		t.Fatalf("alert mails %+v", mails)
	}
	chat := a.chat.Sent()
	if len(chat) != 1 || chat[0].ChatID != oncallChatID || !strings.Contains(chat[0].Text, "placement:p-1") {
		t.Fatalf("chat messages %+v", chat)
	}
	a.checkLogs("worker started after downtime", "overdue job expired", "alert raised", "alert sent", "job started", "job finished")
}

func TestInboundEventDeduplicated(t *testing.T) {
	a := newSchedulerApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	a.mods.platform.dispatcher.Handle(inbound.SourceMAX, "bot_added", func(context.Context, inbound.Event) error {
		calls.Add(1)
		return nil
	})
	rec := inbound.NewRecorder(a.db.Pool, tx.NewRunner(a.db.Pool, a.in.log), a.queue(), a.clk, a.in.log)
	body := []byte(`{"update_type":"bot_added","chat_id":1}`)
	for range 2 {
		if _, err := rec.Record(ctx, inbound.Event{Source: inbound.SourceMAX, ExternalKey: inbound.KeyFromBody(body), EventType: "bot_added", Payload: body}); err != nil {
			t.Fatal(err)
		}
	}
	other := []byte(`{"update_type":"user_added"}`)
	_, _ = rec.Record(ctx, inbound.Event{Source: inbound.SourceMAX, ExternalKey: inbound.KeyFromBody(other), EventType: "user_added", Payload: other})
	a.drain()
	if calls.Load() != 1 {
		t.Fatalf("handler called %d times", calls.Load())
	}
	if n := a.count(`SELECT count(*) FROM inbound_event WHERE status = 'processed'`); n != 1 {
		t.Fatalf("processed %d", n)
	}
	if n := a.count(`SELECT count(*) FROM inbound_event WHERE status = 'ignored'`); n != 1 {
		t.Fatalf("ignored %d", n)
	}
	a.checkLogs("inbound event accepted", "inbound event duplicate", "inbound event processed", "inbound event ignored")
}

func TestCircuitOpensAndAlertsOnce(t *testing.T) {
	a := newSchedulerApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	noSleep := func(context.Context, time.Duration) error { return nil }
	out := outbound.New(a.db.Pool, a.clk, a.in.log, outbound.Options{System: outbound.SystemMAX, Alerter: a.in.alerter, Sleep: noSleep})
	lim := ratelimit.New(a.db.Pool, a.clk, a.in.log, ratelimit.Options{Sleep: func(_ context.Context, d time.Duration) error {
		a.clk.Advance(d)
		return nil
	}})
	client := max.New(out, lim, max.Options{BaseURL: srv.URL, Token: botToken}, a.in.log)
	for range 5 {
		_, _ = client.SendUserMessage(ctx, 42, "Новый запрос на размещение")
	}
	if n := a.count(`SELECT count(*) FROM outbound_request`); n != 10 {
		t.Fatalf("%d requests before the circuit opened, want 10", n)
	}
	if n := a.count(`SELECT count(*) FROM alert WHERE reason = 'circuit_open:max'`); n != 1 {
		t.Fatalf("circuit alerts %d", n)
	}
	a.drain()
	circuitMails := 0
	for _, m := range a.mail.Alerts() {
		if m.Notice.Reason == "circuit_open:max" {
			circuitMails++
		}
	}
	if circuitMails != 1 {
		t.Fatalf("circuit alert mails %d, want 1", circuitMails)
	}
	if n := a.count(`SELECT count(*) FROM outbound_request WHERE request::text LIKE '%' || $1 || '%'`, botToken); n != 0 {
		t.Fatal("bot token stored in outbound_request")
	}
	a.checkLogs("outbound call failed, will retry", "circuit breaker opened", "outbound call skipped, circuit open")
}

func TestStuckInboundEventAlertedOnce(t *testing.T) {
	a := newSchedulerApp(t)
	ctx := context.Background()
	if _, err := a.db.Pool.Exec(ctx, `INSERT INTO inbound_event (id, source, external_key, event_type, payload, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'max', 'k', 'bot_added', '{}', 'received', $1, $1)`, testStart); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(11 * time.Minute)
	if err := a.mods.platform.stuck.Run(ctx, job.Running{}); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(30 * time.Minute)
	if err := a.mods.platform.stuck.Run(ctx, job.Running{}); err != nil {
		t.Fatal(err)
	}
	if n := a.count(`SELECT count(*) FROM job WHERE kind = 'send_alert'`); n != 2 { // одно оповещение: почта и чат
		t.Fatalf("send_alert jobs %d, want 2", n)
	}
	a.checkLogs("stuck objects check finished")
}

// checkLogs проверяет обязательные события журнала (07 §5.3) и отсутствие
// секретов и персональных данных в открытом виде (07 §5.4).
func (a *schedulerApp) checkLogs(messages ...string) {
	a.t.Helper()
	out := a.logs.String()
	for _, m := range messages {
		if !strings.Contains(out, `"msg":"`+m+`"`) {
			a.t.Errorf("log has no %q", m)
		}
	}
	for _, secret := range []string{botToken, oncallEmail, "Authorization"} {
		if strings.Contains(out, secret) {
			a.t.Errorf("log contains %q", secret)
		}
	}
}
