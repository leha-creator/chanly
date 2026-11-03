package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
)

type raised struct {
	reason, objectKey string
	fields            map[string]any
}

type fakeAlerter struct {
	mu  sync.Mutex
	all []raised
}

func (f *fakeAlerter) RaiseError(_ context.Context, reason, objectKey, _ string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.all = append(f.all, raised{reason, objectKey, fields})
}

func (f *fakeAlerter) list() []raised {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]raised(nil), f.all...)
}

func placementRef(payload []byte) string {
	var p struct {
		PlacementID string `json:"placement_id"`
	}
	_ = json.Unmarshal(payload, &p)
	return "placement:" + p.PlacementID
}

func TestDowntimeDetected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := e.worker()
	if _, err := old.CheckDowntime(ctx); err != nil { // первая отметка в пустой базе — не простой
		t.Fatal(err)
	}
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "publish", MaxAttempts: 1, RunAt: start.Add(5 * time.Minute),
		Payload: map[string]string{"placement_id": "p1"}})
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "metrics", MaxAttempts: 1, RunAt: start.Add(5 * time.Minute)})

	e.clk.Advance(40 * time.Minute)
	alerts := &fakeAlerter{}
	w := e.worker()
	w.SetAlerter(alerts)
	w.Register("publish", func(context.Context, job.Running) error { return nil },
		job.HandlerOptions{Priority: job.PriorityHigh, ObjectRef: placementRef})
	w.Register("metrics", func(context.Context, job.Running) error { return nil }, job.HandlerOptions{})
	gap, err := w.CheckDowntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gap != 40*time.Minute {
		t.Fatalf("gap %v", gap)
	}
	got := alerts.list()
	if len(got) != 1 || got[0].reason != "downtime" {
		t.Fatalf("alerts %+v", got)
	}
	objects, _ := got[0].fields["affected_objects"].([]string)
	if len(objects) != 1 || objects[0] != "placement:p1" {
		t.Fatalf("affected objects %v", got[0].fields["affected_objects"])
	}
}

func TestNoDowntimeWithRecentHeartbeat(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.worker().Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(5 * time.Minute)
	alerts := &fakeAlerter{}
	w := e.worker()
	w.SetAlerter(alerts)
	if gap, err := w.CheckDowntime(ctx); err != nil || gap != 0 {
		t.Fatalf("gap %v err %v", gap, err)
	}
	if len(alerts.list()) != 0 {
		t.Fatal("alert without downtime")
	}
}

func TestOverdueExpire(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var handled, expiredCalls atomic.Int32
	w := e.worker()
	w.Register("publish", func(context.Context, job.Running) error { handled.Add(1); return nil },
		job.HandlerOptions{Overdue: job.Overdue{After: 30 * time.Minute, OnExpired: func(context.Context, job.Running) error {
			expiredCalls.Add(1)
			return nil
		}}})

	_ = e.queue.Enqueue(ctx, job.Job{Kind: "publish", MaxAttempts: 1})
	e.clk.Advance(29 * time.Minute)
	_, _ = w.RunOnce(ctx)
	if handled.Load() != 1 || expiredCalls.Load() != 0 {
		t.Fatalf("29 min: handled=%d expired=%d", handled.Load(), expiredCalls.Load())
	}

	_ = e.queue.Enqueue(ctx, job.Job{Kind: "publish", MaxAttempts: 1})
	e.clk.Advance(31 * time.Minute)
	_, _ = w.RunOnce(ctx)
	if handled.Load() != 1 || expiredCalls.Load() != 1 {
		t.Fatalf("31 min: handled=%d expired=%d", handled.Load(), expiredCalls.Load())
	}
	if j := e.jobs(t)[1]; j.Status != "succeeded" {
		t.Fatalf("expired job status %s", j.Status)
	}
}

func TestExhaustedAttemptsRaiseAlertAndHook(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_ = e.queue.Enqueue(ctx, job.Job{Kind: "sync", MaxAttempts: 1})
	alerts := &fakeAlerter{}
	var hookErr error
	w := e.worker()
	w.SetAlerter(alerts)
	w.Register("sync", func(context.Context, job.Running) error { return errors.New("ord down") },
		job.HandlerOptions{OnExhausted: func(_ context.Context, _ job.Running, err error) { hookErr = err }})
	_, _ = w.RunOnce(ctx)
	got := alerts.list()
	if len(got) != 1 || got[0].reason != "job_failed:sync" || got[0].objectKey != e.jobs(t)[0].ID.String() {
		t.Fatalf("alerts %+v", got)
	}
	if hookErr == nil || hookErr.Error() != "ord down" {
		t.Fatalf("OnExhausted err %v", hookErr)
	}
}
