package domain

import (
	"math"
	"time"
)

// ViewsCheckpoint contains only the last real sample, bounded to the read window.
// DayInvalid preserves an unknown daily total through later same-day refreshes.
type ViewsCheckpoint struct {
	ObservedAt time.Time
	Complete   bool
	DayInvalid bool
	Counters   map[string]int64
}

// ViewsGrowth is the daily accumulated growth and the next per-message baseline.
type ViewsGrowth struct {
	Checkpoint  ViewsCheckpoint
	NewViews    *int64
	PeriodStart *time.Time
}

// ComputeViewsGrowth never derives growth from averages or moving-window totals.
// Disappearing posts contribute nothing; newly published posts can contribute
// their entire counter only when published after the preceding observation.
func ComputeViewsGrowth(prev *ViewsCheckpoint, daily *MetricsSnapshot, messages []MessageStat, at time.Time, complete bool, window time.Duration) ViewsGrowth {
	current := ViewsCheckpoint{ObservedAt: at, Complete: complete, Counters: make(map[string]int64, len(messages))}
	posts := make(map[string]MessageStat, len(messages))
	for _, m := range messages {
		if m.At.After(at) || at.Sub(m.At) > window {
			continue
		}
		if m.ID == "" || m.Views == nil || *m.Views < 0 {
			current.Complete = false
			continue
		}
		v := int64(*m.Views)
		if old, ok := current.Counters[m.ID]; ok && old != v {
			current.Complete = false
		}
		current.Counters[m.ID], posts[m.ID] = v, m
	}
	result := ViewsGrowth{Checkpoint: current}
	if prev == nil {
		result.Checkpoint.DayInvalid = !current.Complete
		return result // A first snapshot cannot reveal any pre-observation growth.
	}
	day, prevDay := MetricsDate(at), MetricsDate(prev.ObservedAt)
	sameDay := day.Equal(prevDay)
	valid := current.Complete && prev.Complete && !prev.ObservedAt.After(at) &&
		(sameDay || day.Equal(prevDay.AddDate(0, 0, 1))) && !(sameDay && prev.DayInvalid)
	var delta int64
	for id, views := range current.Counters {
		before, known := prev.Counters[id]
		if !known {
			if !posts[id].At.After(prev.ObservedAt) {
				valid = false // Older unseen posts are a new baseline, never new views.
				continue
			}
			before = 0
		}
		if views < before || delta > math.MaxInt64-(views-before) {
			valid = false
			continue
		}
		delta += views - before
	}
	if !valid {
		result.Checkpoint.DayInvalid = true
		return result
	}
	periodStart := prev.ObservedAt
	if sameDay && daily != nil && daily.NewViews != nil {
		if delta > math.MaxInt64-*daily.NewViews {
			result.Checkpoint.DayInvalid = true
			return result
		}
		delta += *daily.NewViews
		if daily.ViewsPeriodStartedAt != nil {
			periodStart = *daily.ViewsPeriodStartedAt
		}
	}
	result.NewViews, result.PeriodStart = &delta, &periodStart
	return result
}
