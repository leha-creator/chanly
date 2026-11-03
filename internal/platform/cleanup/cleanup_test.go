package cleanup_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/cleanup"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var now = time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func count(t *testing.T, d *dbtest.DB, sql string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRetention(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 2 500 старых исходящих запросов и один свежий.
	exec(`INSERT INTO outbound_request (id, system, operation, attempt, request, created_at)
		SELECT gen_random_uuid(), 'max', 'GetMe', 1, '{}', $1 FROM generate_series(1, 2500)`, now.Add(-181*day))
	exec(`INSERT INTO outbound_request (id, system, operation, attempt, request, created_at)
		VALUES (gen_random_uuid(), 'max', 'GetMe', 1, '{}', $1)`, now.Add(-179*day))
	addJob := func(status string, age time.Duration) {
		exec(`INSERT INTO job (id, kind, payload, run_at, status, max_attempts, finished_at, created_at, updated_at)
			VALUES (gen_random_uuid(), 'x', '{}', $1, $2, 1, $1, $1, $1)`, now.Add(-age), status)
	}
	addJob("succeeded", 31*day)
	addJob("succeeded", 29*day)
	addJob("cancelled", 31*day)
	addJob("failed", 31*day)
	addJob("failed", 181*day)
	addEvent := func(key, status string, age time.Duration) {
		exec(`INSERT INTO inbound_event (id, source, external_key, event_type, payload, status, created_at, updated_at)
			VALUES (gen_random_uuid(), 'max', $1, 'x', '{}', $2, $3, $3)`, key, status, now.Add(-age))
	}
	addEvent("a", "processed", 181*day)
	addEvent("b", "received", 181*day) // необработанное не удаляется
	addEvent("c", "ignored", 10*day)

	c := cleanup.New(clock.NewManual(now), logx.Discard())
	c.Add(cleanup.Task{Name: "outbound_request", Retention: 180 * day, Delete: outbound.DeleteRequests(d.Pool)})
	c.Add(cleanup.Task{Name: "job_finished", Retention: 30 * day, Delete: job.DeleteFinished(d.Pool, "succeeded", "cancelled")})
	c.Add(cleanup.Task{Name: "job_failed", Retention: 180 * day, Delete: job.DeleteFinished(d.Pool, "failed")})
	c.Add(cleanup.Task{Name: "inbound_event", Retention: 180 * day, Delete: inbound.DeleteEvents(d.Pool)})
	if err := c.Run(ctx, job.Running{}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM outbound_request`); n != 1 {
		t.Fatalf("outbound_request left %d, want 1", n)
	}
	if n := count(t, d, `SELECT count(*) FROM job`); n != 2 { // succeeded 29 суток и failed 31 сутки
		t.Fatalf("job left %d, want 2", n)
	}
	if n := count(t, d, `SELECT count(*) FROM inbound_event`); n != 2 {
		t.Fatalf("inbound_event left %d, want 2", n)
	}
}

func TestFailingTaskDoesNotStopOthers(t *testing.T) {
	c := cleanup.New(clock.NewManual(now), logx.Discard())
	ran := false
	c.Add(cleanup.Task{Name: "broken", Delete: func(context.Context, time.Time, int32) (int64, error) { return 0, errors.New("boom") }})
	c.Add(cleanup.Task{Name: "ok", Delete: func(context.Context, time.Time, int32) (int64, error) { ran = true; return 0, nil }})
	if err := c.Run(context.Background(), job.Running{}); err == nil || !ran {
		t.Fatalf("err %v ran %v", err, ran)
	}
}
