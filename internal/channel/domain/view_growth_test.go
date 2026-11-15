package domain

import (
	"testing"
	"time"
)

func TestViewsGrowthCountsObservedDeltas(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	old := at.Add(-48 * time.Hour)
	post := func(id string, published time.Time, n int) MessageStat {
		return MessageStat{ID: id, At: published, Views: &n}
	}
	var state *ViewsCheckpoint
	var daily *MetricsSnapshot
	observe := func(when time.Time, messages []MessageStat, want *int64) {
		t.Helper()
		g := ComputeViewsGrowth(state, daily, messages, when, true, 30*24*time.Hour)
		if (g.NewViews == nil) != (want == nil) || (want != nil && *g.NewViews != *want) {
			t.Fatalf("at %v: growth=%v want=%v", when, g.NewViews, want)
		}
		if g.NewViews != nil && (g.PeriodStart == nil || g.PeriodStart.After(when)) {
			t.Fatalf("missing actual observation period: %+v", g)
		}
		state = &g.Checkpoint
		daily = &MetricsSnapshot{Date: MetricsDate(when), NewViews: g.NewViews, ViewsPeriodStartedAt: g.PeriodStart}
	}
	n := func(v int64) *int64 { return &v }
	observe(at, []MessageStat{post("old", old, 5)}, nil)
	observe(at.Add(24*time.Hour), []MessageStat{post("old", old, 5)}, n(0))
	observe(at.Add(25*time.Hour), []MessageStat{post("old", old, 8)}, n(3))
	observe(at.Add(26*time.Hour), []MessageStat{post("old", old, 8)}, n(3))
	if !daily.ViewsPeriodStartedAt.Equal(at) {
		t.Fatalf("same-day accumulation lost earliest observation: %v", daily.ViewsPeriodStartedAt)
	}
	newPostAt := at.Add(26*time.Hour + time.Minute)
	observe(at.Add(27*time.Hour), []MessageStat{post("old", old, 9), post("new", newPostAt, 4)}, n(8))
	// A removed post never subtracts its historic total. Only the available
	// post's newly observed increment is added to the same-day accumulator.
	observe(at.Add(28*time.Hour), []MessageStat{post("new", newPostAt, 6)}, n(10))
}

func TestViewsGrowthUnknownAndRecovery(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	posts := func(n int) []MessageStat { return []MessageStat{{ID: "old", At: at.Add(-48 * time.Hour), Views: &n}} }
	baseline := ComputeViewsGrowth(nil, nil, posts(8), at, true, 30*24*time.Hour)
	for _, tc := range []struct {
		name     string
		messages []MessageStat
		complete bool
		when     time.Time
	}{
		{"reset", posts(2), true, at.Add(time.Hour)},
		{"missing counter", []MessageStat{{ID: "old", At: at.Add(-48 * time.Hour)}}, true, at.Add(time.Hour)},
		{"truncated read", posts(10), false, at.Add(time.Hour)},
		{"unseen old post", []MessageStat{{ID: "different", At: at.Add(-48 * time.Hour), Views: posts(5)[0].Views}}, true, at.Add(time.Hour)},
		{"gap", posts(10), true, at.Add(72 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unknown := ComputeViewsGrowth(&baseline.Checkpoint, nil, tc.messages, tc.when, tc.complete, 30*24*time.Hour)
			if unknown.NewViews != nil || unknown.PeriodStart != nil || !unknown.Checkpoint.DayInvalid {
				t.Fatalf("invented growth: %+v", unknown)
			}
			// An incomplete daily interval stays unknown through later retries.
			again := ComputeViewsGrowth(&unknown.Checkpoint, nil, posts(12), tc.when.Add(time.Hour), true, 30*24*time.Hour)
			if again.NewViews != nil || !again.Checkpoint.DayInvalid {
				t.Fatalf("unknown daily period became known: %+v", again)
			}
			// Once rebased, a following consecutive complete day can resume.
			recovered := ComputeViewsGrowth(&again.Checkpoint, nil, posts(14), tc.when.Add(24*time.Hour), true, 30*24*time.Hour)
			if recovered.NewViews == nil || *recovered.NewViews != 2 {
				t.Fatalf("recovery did not use latest baseline: %+v", recovered)
			}
		})
	}
}

func TestViewsGrowthWindowExpiryNeverSubtractsCounters(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	views := 100
	first := ComputeViewsGrowth(nil, nil, []MessageStat{{ID: "expires", At: at.Add(-30 * 24 * time.Hour), Views: &views}}, at, true, 30*24*time.Hour)
	second := ComputeViewsGrowth(&first.Checkpoint, nil, nil, at.Add(24*time.Hour), true, 30*24*time.Hour)
	if second.NewViews == nil || *second.NewViews != 0 || len(second.Checkpoint.Counters) != 0 {
		t.Fatalf("window expiry changed growth: %+v", second)
	}
}
