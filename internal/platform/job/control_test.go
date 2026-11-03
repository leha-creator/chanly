package job_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestRequestedRetryDelay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "publish", MaxAttempts: job.Unlimited})
	w := e.worker()
	w.Register("publish", func(context.Context, job.Running) error {
		return job.Retry(errors.New("channel busy"), 30*time.Second)
	}, job.HandlerOptions{})
	for i := range 3 {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		j := e.jobs(t)[0]
		if j.Status != "pending" || int(j.Attempts) != i+1 || !j.RunAt.Equal(e.clk.Now().Add(30*time.Second)) {
			t.Fatalf("attempt %d: %+v", i+1, j)
		}
		e.clk.Advance(30 * time.Second)
	}
}

func TestRequestedRetryCountsAttempts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "x", MaxAttempts: 1})
	w := e.worker()
	w.Register("x", func(context.Context, job.Running) error { return job.Retry(errors.New("later"), time.Second) }, job.HandlerOptions{})
	_, _ = w.RunOnce(ctx)
	if j := e.jobs(t)[0]; j.Status != "failed" {
		t.Fatalf("retry beyond max_attempts must fail, got %s", j.Status)
	}
}

func TestPermanentFailsAtOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "x", MaxAttempts: 10})
	w := e.worker()
	w.Register("x", func(context.Context, job.Running) error { return job.Permanent(errors.New("400 bad request")) }, job.HandlerOptions{})
	_, _ = w.RunOnce(ctx)
	j := e.jobs(t)[0]
	if j.Status != "failed" || j.Attempts != 1 || *j.LastError != "400 bad request" {
		t.Fatalf("permanent: %+v", j)
	}
}

func TestCancelAndReschedule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boom := errors.New("boom")
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "timer", DedupeKey: "timer:1", MaxAttempts: 1, RunAt: start.Add(time.Hour)})

	// Отмена в откатившейся транзакции не действует (I-10).
	err := e.tx.InTx(ctx, func(ctx context.Context) error {
		if ok, err := e.queue.Cancel(ctx, "timer:1"); err != nil || !ok {
			t.Fatalf("cancel in tx: %v %v", ok, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if j := e.jobs(t)[0]; j.Status != "pending" {
		t.Fatalf("cancel survived rollback: %s", j.Status)
	}

	if ok, err := e.queue.Reschedule(ctx, "timer:1", start.Add(2*time.Hour)); err != nil || !ok {
		t.Fatalf("reschedule: %v %v", ok, err)
	}
	if j := e.jobs(t)[0]; !j.RunAt.Equal(start.Add(2 * time.Hour)) {
		t.Fatalf("run_at %v", j.RunAt)
	}
	if ok, err := e.queue.Cancel(ctx, "timer:1"); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	j := e.jobs(t)[0]
	if j.Status != "cancelled" || j.FinishedAt == nil {
		t.Fatalf("after cancel: %+v", j)
	}
	if ok, _ := e.queue.Cancel(ctx, "timer:1"); ok {
		t.Fatal("second cancel found a job")
	}
	// Ключ освобождён: можно поставить новое задание.
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "timer", DedupeKey: "timer:1", MaxAttempts: 1})
	if n := len(e.jobs(t)); n != 2 {
		t.Fatalf("%d jobs, want 2", n)
	}
}

func TestCancelDoesNotTouchRunning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "x", DedupeKey: "x:1", MaxAttempts: 1})
	if _, err := e.d.Owner.Exec(ctx, `UPDATE job SET status = 'running', locked_by = 'w', locked_at = $1`, start); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.queue.Cancel(ctx, "x:1"); ok {
		t.Fatal("running job cancelled")
	}
	if ok, _ := e.queue.Reschedule(ctx, "x:1", start.Add(time.Hour)); ok {
		t.Fatal("running job rescheduled")
	}
}

func TestHighPriorityFirstAndReserve(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for range 8 {
		_ = e.queue.Enqueue(ctx, job.Job{Kind: "metrics", MaxAttempts: 1})
	}
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "publish", MaxAttempts: 1, RunAt: start.Add(time.Second)})
	e.clk.Advance(time.Second)

	var low, high atomic.Int32
	release := make(chan struct{})
	w := e.worker()
	w.Register("metrics", func(context.Context, job.Running) error { low.Add(1); return nil }, job.HandlerOptions{})
	w.Register("publish", func(context.Context, job.Running) error {
		<-release // высокоприоритетное задание держит место, пока берутся остальные
		high.Add(1)
		return nil
	}, job.HandlerOptions{Priority: job.PriorityHigh})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 8 мест: 1 — публикация, 2 — резерв высокого приоритета, обычным — 5.
	if high.Load() != 1 || low.Load() != 5 {
		t.Fatalf("high=%d low=%d, want 1 and 5", high.Load(), low.Load())
	}
}

func TestNoReserveWithoutHighPriorityKinds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for range 8 {
		_ = e.queue.Enqueue(ctx, job.Job{Kind: "metrics", MaxAttempts: 1})
	}
	w := e.worker()
	w.Register("metrics", func(context.Context, job.Running) error { return nil }, job.HandlerOptions{})
	if n, _ := w.RunOnce(ctx); n != 8 {
		t.Fatalf("started %d, want 8", n)
	}
}

func TestLagMetric(t *testing.T) {
	e := newEnv(t)
	_ = e.queue.Enqueue(context.Background(), job.Job{Kind: "publish", MaxAttempts: 1})
	e.clk.Advance(12 * time.Second)
	reg := prometheus.NewRegistry()
	reg.MustRegister(job.NewCollector(e.d.Pool, e.clk, logx.Discard()))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "maxim_jobs_lag_seconds" {
			if v := mf.GetMetric()[0].GetGauge().GetValue(); v != 12 {
				t.Fatalf("lag %v, want 12", v)
			}
			return
		}
	}
	t.Fatal("maxim_jobs_lag_seconds not collected")
}
