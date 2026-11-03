package inbound_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	d        *dbtest.DB
	clk      *clock.Manual
	recorder *inbound.Recorder
	disp     *inbound.Dispatcher
	worker   *job.Worker
}

func newEnv(t *testing.T) env {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	log := logx.Discard()
	queue := job.NewQueue(d.Pool, clk, log)
	e := env{d: d, clk: clk,
		recorder: inbound.NewRecorder(d.Pool, tx.NewRunner(d.Pool, log), queue, clk, log),
		disp:     inbound.NewDispatcher(d.Pool, clk, log),
		worker:   job.NewWorker(d.Pool, clk, log),
	}
	e.disp.Register(e.worker)
	return e
}

func (e env) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) status(t *testing.T) string {
	t.Helper()
	var s string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT status FROM inbound_event`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

var botAdded = inbound.Event{Source: inbound.SourceMAX, ExternalKey: "evt-1", EventType: "bot_added", Payload: []byte(`{"chat_id":1}`)}

func TestDuplicateEventRecordedOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var calls atomic.Int32
	e.disp.Handle(inbound.SourceMAX, "bot_added", func(_ context.Context, ev inbound.Event) error {
		if string(ev.Payload) != `{"chat_id": 1}` && string(ev.Payload) != `{"chat_id":1}` {
			t.Errorf("payload %s", ev.Payload)
		}
		calls.Add(1)
		return nil
	})
	for i, wantDup := range []bool{false, true} {
		dup, err := e.recorder.Record(ctx, botAdded)
		if err != nil || dup != wantDup {
			t.Fatalf("record %d: dup=%v err=%v", i, dup, err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM inbound_event`); n != 1 {
		t.Fatalf("%d events", n)
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE kind = 'process_inbound_event'`); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	if _, err := e.worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || e.status(t) != inbound.StatusProcessed {
		t.Fatalf("calls %d status %s", calls.Load(), e.status(t))
	}
	// Повторный запуск задания по обработанному событию обработчик не вызывает.
	if _, err := e.d.Owner.Exec(ctx, `UPDATE job SET status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler called %d times on replay", calls.Load())
	}
}

func TestUnknownEventIgnored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _ = e.recorder.Record(ctx, inbound.Event{Source: inbound.SourceMAX, ExternalKey: inbound.KeyFromBody([]byte(`{}`)),
		EventType: "user_added", Payload: []byte(`{}`)})
	_, _ = e.worker.RunOnce(ctx)
	if s := e.status(t); s != inbound.StatusIgnored {
		t.Fatalf("status %s", s)
	}
}

func TestFailedAfterAttempts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.disp.Handle(inbound.SourceMAX, "bot_added", func(context.Context, inbound.Event) error { return errors.New("channel module down") })
	_, _ = e.recorder.Record(ctx, botAdded)
	for range 10 {
		if _, err := e.worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(time.Hour)
	}
	if s := e.status(t); s != inbound.StatusFailed {
		t.Fatalf("status after 10 failures: %s", s)
	}
	var msg string
	_ = e.d.Pool.QueryRow(ctx, `SELECT error FROM inbound_event`).Scan(&msg)
	if msg != "channel module down" {
		t.Fatalf("error %q", msg)
	}
}

func TestRecordValidates(t *testing.T) {
	e := newEnv(t)
	if _, err := e.recorder.Record(context.Background(), inbound.Event{Source: "max", ExternalKey: "k", EventType: "x", Payload: []byte("not json")}); err == nil {
		t.Fatal("non-JSON payload accepted")
	}
}

func TestOldestUnprocessedMetric(t *testing.T) {
	e := newEnv(t)
	_, _ = e.recorder.Record(context.Background(), botAdded)
	e.clk.Advance(90 * time.Second)
	reg := prometheus.NewRegistry()
	reg.MustRegister(inbound.NewCollector(e.d.Pool, e.clk, logx.Discard()))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) != 1 || len(mfs[0].GetMetric()) != 1 || mfs[0].GetMetric()[0].GetGauge().GetValue() != 90 {
		t.Fatalf("metrics %v", mfs)
	}
}
