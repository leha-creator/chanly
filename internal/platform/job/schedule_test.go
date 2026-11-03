package job_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/job"
)

var mskZone = time.FixedZone("MSK", 3*3600)

func msk(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, mskZone).UTC()
}

func TestScheduleNext(t *testing.T) {
	cases := []struct {
		name  string
		s     job.Schedule
		after time.Time
		want  time.Time
	}{
		{"every", job.Every(5 * time.Minute), msk(2026, 10, 16, 12, 0), msk(2026, 10, 16, 12, 5)},
		{"daily later today", job.DailyAt(4, 0), msk(2026, 10, 16, 3, 59), msk(2026, 10, 16, 4, 0)},
		{"daily exactly now is tomorrow", job.DailyAt(4, 0), msk(2026, 10, 16, 4, 0), msk(2026, 10, 17, 4, 0)},
		// 23:30 UTC 16-го — уже 02:30 17-го по Москве.
		{"daily across utc midnight", job.DailyAt(4, 0), time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC), msk(2026, 10, 17, 4, 0)},
		{"monthly next month", job.MonthlyAt(1, 3, 0), msk(2026, 10, 16, 12, 0), msk(2026, 11, 1, 3, 0)},
		{"monthly year end", job.MonthlyAt(1, 3, 0), msk(2026, 12, 1, 3, 0), msk(2027, 1, 1, 3, 0)},
		{"monthly 31 in short month", job.MonthlyAt(31, 0, 0), msk(2027, 1, 31, 0, 0), msk(2027, 2, 28, 0, 0)},
		{"monthly 31 in long month", job.MonthlyAt(31, 0, 0), msk(2027, 2, 28, 0, 0), msk(2027, 3, 31, 0, 0)},
	}
	for _, c := range cases {
		if got := c.s.Next(c.after); !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("%s: Next(%v) = %v, want %v", c.name, c.after, got, c.want)
		}
	}
}

func TestSpread(t *testing.T) {
	day := 24 * time.Hour
	a := job.Spread(job.DailyAt(0, 0), "channel:1", day)
	b := job.Spread(job.DailyAt(0, 0), "channel:1", day)
	after := msk(2026, 10, 16, 12, 0)
	if !a.Next(after).Equal(b.Next(after)) {
		t.Fatal("spread is not stable for the same key")
	}
	seen := map[time.Time]bool{}
	for i := range 20 {
		s := job.Spread(job.DailyAt(0, 0), "channel:"+string(rune('a'+i)), day)
		next := s.Next(after)
		if !next.After(after) || next.Sub(after) > day {
			t.Fatalf("next %v outside (after, after+day]", next)
		}
		if got := s.Next(next); got.Sub(next) != day {
			t.Fatalf("interval %v, want a day", got.Sub(next))
		}
		seen[next] = true
	}
	if len(seen) < 15 {
		t.Fatalf("only %d distinct start times for 20 keys", len(seen))
	}
}

func TestPeriodicSingleJobAcrossWorkers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for range 2 {
		w := e.worker()
		w.RegisterPeriodic("cleanup", job.Every(time.Hour), func(context.Context, job.Running) error { return nil }, job.HandlerOptions{})
		if err := w.EnsurePeriodic(ctx); err != nil {
			t.Fatal(err)
		}
	}
	jobs := e.jobs(t)
	if len(jobs) != 1 || !jobs[0].RunAt.Equal(start.Add(time.Hour)) || *jobs[0].DedupeKey != "periodic:cleanup" || jobs[0].MaxAttempts != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
}

func TestPeriodicNextRunRegardlessOfOutcomeAndNoCatchUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var calls atomic.Int32
	w := e.worker()
	w.RegisterPeriodic("reconcile", job.Every(5*time.Minute), func(context.Context, job.Running) error {
		calls.Add(1)
		return errors.New("max unavailable")
	}, job.HandlerOptions{})
	if err := w.EnsurePeriodic(ctx); err != nil {
		t.Fatal(err)
	}
	// Простой на 5 интервалов: выполняется один запуск, без догоняющих.
	e.clk.Advance(25 * time.Minute)
	for range 3 {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("handler called %d times after downtime, want 1", calls.Load())
	}
	jobs := e.jobs(t)
	if len(jobs) != 2 || jobs[0].Status != "failed" || jobs[1].Status != "pending" || !jobs[1].RunAt.Equal(e.clk.Now().Add(5*time.Minute)) {
		t.Fatalf("after failed run: %+v", jobs)
	}
}
