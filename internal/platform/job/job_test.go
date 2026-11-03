package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	d     *dbtest.DB
	clk   *clock.Manual
	queue *job.Queue
	tx    tx.Runner
}

func newEnv(t *testing.T) env {
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	return env{d: d, clk: clk, queue: job.NewQueue(d.Pool, clk, logx.Discard()), tx: tx.NewRunner(d.Pool, logx.Discard())}
}

func (e env) worker() *job.Worker { return job.NewWorker(e.d.Pool, e.clk, logx.Discard()) }

func (e env) jobs(t *testing.T) []sqlc.Job {
	t.Helper()
	rows, err := e.d.Pool.Query(context.Background(), `SELECT id FROM job ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sqlc.Job
	q := sqlc.New(e.d.Pool)
	for rows.Next() {
		var j sqlc.Job
		if err := rows.Scan(&j.ID); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	for i := range out {
		j, err := q.GetJob(context.Background(), out[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = j
	}
	return out
}

func TestEnqueueRollsBackWithTransaction(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("boom")
	err := e.tx.InTx(context.Background(), func(ctx context.Context) error {
		if err := e.queue.Enqueue(ctx, job.Job{Kind: "test", MaxAttempts: 1}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n := len(e.jobs(t)); n != 0 {
		t.Fatalf("%d jobs after rollback", n)
	}
}

func TestEnqueueDedupe(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := job.Job{Kind: "test", DedupeKey: "test:1", MaxAttempts: 1}
	for range 2 {
		if err := e.queue.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.jobs(t)); n != 1 {
		t.Fatalf("%d jobs, want 1", n)
	}
	// После завершения задания ключ освобождается.
	w := e.worker()
	w.Register("test", func(context.Context, job.Running) error { return nil }, job.HandlerOptions{})
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.queue.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	if n := len(e.jobs(t)); n != 2 {
		t.Fatalf("%d jobs, want 2", n)
	}
}

func TestRetryAndFail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.queue.Enqueue(ctx, job.Job{Kind: "flaky", MaxAttempts: 3, Payload: map[string]string{"secret": "x"}}); err != nil {
		t.Fatal(err)
	}
	w := e.worker()
	var calls atomic.Int32
	w.Register("flaky", func(context.Context, job.Running) error {
		calls.Add(1)
		return errors.New("temporary")
	}, job.HandlerOptions{WipePayload: true})

	if n, err := w.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	j := e.jobs(t)[0]
	if j.Status != "pending" || j.Attempts != 1 || !j.RunAt.Equal(start.Add(30*time.Second)) || *j.LastError != "temporary" {
		t.Fatalf("after first failure: %+v", j)
	}
	// Раньше срока повтора задание не берётся.
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("job retried before run_at")
	}
	e.clk.Advance(30 * time.Second)
	if n, _ := w.RunOnce(ctx); n != 1 {
		t.Fatal("second run did not happen")
	}
	j = e.jobs(t)[0]
	if j.Attempts != 2 || !j.RunAt.Equal(e.clk.Now().Add(60*time.Second)) {
		t.Fatalf("after second failure: %+v", j)
	}
	e.clk.Advance(time.Minute)
	if n, _ := w.RunOnce(ctx); n != 1 {
		t.Fatal("third run did not happen")
	}
	j = e.jobs(t)[0]
	if j.Status != "failed" || j.Attempts != 3 || j.FinishedAt == nil || string(j.Payload) != "{}" {
		t.Fatalf("after exhausting attempts: %+v payload=%s", j, j.Payload)
	}
	if calls.Load() != 3 {
		t.Fatalf("handler called %d times", calls.Load())
	}
}

func TestSuccessPropagatesRequestIDAndWipesPayload(t *testing.T) {
	e := newEnv(t)
	ctx := logx.WithRequestID(context.Background(), "req-42")
	type payload struct {
		Value string `json:"value"`
	}
	if err := e.queue.Enqueue(ctx, job.Job{Kind: "ok", MaxAttempts: 1, Payload: payload{Value: "v"}}); err != nil {
		t.Fatal(err)
	}
	var gotRequestID string
	var got payload
	var raw []byte
	w := e.worker()
	w.Register("ok", func(ctx context.Context, r job.Running) error {
		gotRequestID = logx.RequestID(ctx)
		raw = r.Payload
		return json.Unmarshal(r.Payload, &got)
	}, job.HandlerOptions{WipePayload: true})
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotRequestID != "req-42" || got.Value != "v" || string(raw) != `{"value":"v"}` {
		t.Fatalf("request id %q payload %s", gotRequestID, raw)
	}
	j := e.jobs(t)[0]
	if j.Status != "succeeded" || j.FinishedAt == nil || string(j.Payload) != "{}" || j.LockedBy != nil {
		t.Fatalf("after success: %+v payload=%s", j, j.Payload)
	}
}

func TestPanicIsAFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "panics", MaxAttempts: 1})
	w := e.worker()
	w.Register("panics", func(context.Context, job.Running) error { panic("boom") }, job.HandlerOptions{})
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.jobs(t)[0]; j.Status != "failed" {
		t.Fatalf("status %s", j.Status)
	}
}

func TestTwoWorkersDoNotShareAJob(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for range 20 {
		_ = e.queue.Enqueue(ctx, job.Job{Kind: "count", MaxAttempts: 1})
	}
	var calls atomic.Int32
	handler := func(context.Context, job.Running) error {
		calls.Add(1)
		time.Sleep(5 * time.Millisecond)
		return nil
	}
	var wg sync.WaitGroup
	for range 2 {
		w := e.worker()
		w.Register("count", handler, job.HandlerOptions{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := w.RunOnce(ctx)
				if err != nil || n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 20 {
		t.Fatalf("handler called %d times for 20 jobs", calls.Load())
	}
}

func TestAbandonedJobReturnsToQueue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "stuck", MaxAttempts: 1})
	if _, err := e.d.Owner.Exec(ctx, `UPDATE job SET status = 'running', locked_by = 'dead:1', locked_at = $1`, start); err != nil {
		t.Fatal(err)
	}
	w := e.worker()
	e.clk.Advance(9 * time.Minute)
	if n := w.ReleaseAbandoned(ctx); n != 0 {
		t.Fatal("job released before 10 minutes")
	}
	e.clk.Advance(2 * time.Minute)
	if n := w.ReleaseAbandoned(ctx); n != 1 {
		t.Fatal("abandoned job was not released")
	}
	if j := e.jobs(t)[0]; j.Status != "pending" || j.LockedBy != nil {
		t.Fatalf("after release: %+v", j)
	}
}

func TestUnknownKindIsNotTaken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "other", MaxAttempts: 1})
	w := e.worker()
	w.Register("mine", func(context.Context, job.Running) error { return nil }, job.HandlerOptions{})
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("worker took a job of an unregistered kind")
	}
}

func TestRetryDelay(t *testing.T) {
	cases := map[int]time.Duration{0: 30 * time.Second, 1: time.Minute, 2: 2 * time.Minute, 6: 32 * time.Minute, 7: time.Hour, 40: time.Hour}
	for attempts, want := range cases {
		if got := job.RetryDelay(attempts); got != want {
			t.Errorf("RetryDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestPostponeDoesNotCountAttempts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.queue.Enqueue(ctx, job.Job{Kind: "waiting", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	w := e.worker()
	var calls atomic.Int32
	w.Register("waiting", func(context.Context, job.Running) error {
		if calls.Add(1) < 4 {
			return job.Postpone(errors.New("dependency not ready"), 30*time.Second)
		}
		return nil
	}, job.HandlerOptions{})
	for i := range 3 {
		if n, _ := w.RunOnce(ctx); n != 1 {
			t.Fatalf("run %d did not happen", i+1)
		}
		j := e.jobs(t)[0]
		if j.Status != "pending" || j.Attempts != 0 || !j.RunAt.Equal(e.clk.Now().Add(30*time.Second)) {
			t.Fatalf("after postpone %d: %+v", i+1, j)
		}
		e.clk.Advance(30 * time.Second)
	}
	if n, _ := w.RunOnce(ctx); n != 1 {
		t.Fatal("final run did not happen")
	}
	if j := e.jobs(t)[0]; j.Status != "succeeded" {
		t.Fatalf("max_attempts 1 exhausted by postpones: %+v", j)
	}
}
