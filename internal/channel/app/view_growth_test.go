package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
)

func setMetricViews(e *env, chatID int64, n int) {
	e.max.mu.Lock()
	defer e.max.mu.Unlock()
	e.max.messages[chatID] = []domain.MessageStat{{ID: "post", At: start.Add(-48 * time.Hour), Views: &n}}
}

func TestViewGrowthPersistsActualObservationsAndSameDayRetries(t *testing.T) {
	e := newEnv(t)
	const chatID = -80000000911
	setMetricViews(e, chatID, 5)
	c, _ := e.linkedChannel(chatID, allRights)
	p := app.MetricsPayload{ChannelID: c.ID, Date: domain.MetricsDate(start).Format(time.DateOnly)}
	latest := func() domain.MetricsSnapshot {
		t.Helper()
		list, err := e.store.Metrics().Recent(t.Context(), c.ID, 1)
		if err != nil || len(list) != 1 {
			t.Fatalf("latest snapshot: %+v %v", list, err)
		}
		return list[0]
	}
	if first := latest(); first.NewViews != nil || first.ObservedAt == nil || first.AvgViews == nil || *first.AvgViews != 5 {
		t.Fatalf("first observation must retain current summary without fake growth: %+v", first)
	}
	e.clk.Advance(24 * time.Hour)
	info := publicCh
	info.SubscribersCount++
	e.max.set(chatID, info, allRights)
	// The queued date is deliberately yesterday. Actual observations belong to
	// today's snapshot and must never rewrite yesterday's subscriber history.
	if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if current := latest(); current.NewViews == nil || *current.NewViews != 0 || !current.Date.Equal(domain.MetricsDate(e.clk.Now())) {
		t.Fatalf("unchanged counter should report real zero today: %+v", current)
	}
	setMetricViews(e, chatID, 8)
	e.clk.Advance(time.Hour)
	for range 2 {
		if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if current := latest(); current.NewViews == nil || *current.NewViews != 3 || !current.ViewsPeriodStartedAt.Equal(start) {
		t.Fatalf("same-day increment/retry: %+v", current)
	}
	previous, err := e.store.Metrics().LastBefore(t.Context(), c.ID, domain.MetricsDate(e.clk.Now()))
	if err != nil || previous == nil || previous.SubscribersCount != publicCh.SubscribersCount || previous.NewViews != nil {
		t.Fatalf("old observation was overwritten: %+v %v", previous, err)
	}
	// A counter update failing after both writes rolls back snapshot and baseline.
	base := e.svc.Metrics
	e.svc.Metrics = failingViewMetrics{Metrics: base}
	setMetricViews(e, chatID, 9)
	e.clk.Advance(time.Hour)
	if err := e.svc.RefreshMetrics(t.Context(), p); !errors.Is(err, errSaveInjected) {
		t.Fatalf("counter failure: %v", err)
	}
	checkpoint, err := base.Checkpoint(t.Context(), c.ID)
	if err != nil || checkpoint.Counters["post"] != 8 || *latest().NewViews != 3 {
		t.Fatalf("failed observation was partially committed: %+v %v", checkpoint, err)
	}
	e.svc.Metrics = base
	if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if current := latest(); current.NewViews == nil || *current.NewViews != 4 {
		t.Fatalf("retry lost/doubled increment: %+v", current)
	}
	// Skipped dates have no observation row; multi-day growth stays unknown.
	e.clk.Advance(72 * time.Hour)
	setMetricViews(e, chatID, 20)
	if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if current := latest(); current.NewViews != nil || current.ViewsPeriodStartedAt != nil {
		t.Fatalf("gap invented daily growth: %+v", current)
	}
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1`, c.ID); n != 3 {
		t.Fatalf("invented daily rows: %d", n)
	}
}

type failingViewMetrics struct{ domain.Metrics }

func (m failingViewMetrics) RecordViews(ctx context.Context, id uuid.UUID, c domain.ViewsCheckpoint) error {
	if err := m.Metrics.RecordViews(ctx, id, c); err != nil {
		return err
	}
	return errSaveInjected
}

type delayedViewReader struct {
	domain.MessageReader
	once             sync.Once
	started, release chan struct{}
}

func (r *delayedViewReader) ListMessages(ctx context.Context, id int64, from, to time.Time, n int) ([]domain.MessageStat, error) {
	list, err := r.MessageReader.ListMessages(ctx, id, from, to, n)
	first := false
	r.once.Do(func() { first = true; close(r.started) })
	if first {
		<-r.release
	}
	return list, err
}

func TestViewGrowthRejectsInterleavedStaleSample(t *testing.T) {
	e := newEnv(t)
	const chatID = -80000000912
	setMetricViews(e, chatID, 5)
	c, _ := e.linkedChannel(chatID, allRights)
	p := app.MetricsPayload{ChannelID: c.ID, Date: domain.MetricsDate(start).Format(time.DateOnly)}
	r := &delayedViewReader{MessageReader: e.max, started: make(chan struct{}), release: make(chan struct{})}
	e.svc.Messages = r
	setMetricViews(e, chatID, 7)
	e.clk.Advance(time.Minute)
	finished := make(chan error, 1)
	go func() { finished <- e.svc.RefreshMetrics(t.Context(), p) }()
	<-r.started
	setMetricViews(e, chatID, 10)
	e.clk.Advance(time.Minute)
	newerAt := e.clk.Now()
	newerErr := e.svc.RefreshMetrics(t.Context(), p)
	close(r.release)
	olderErr := <-finished
	if newerErr != nil || olderErr != nil {
		t.Fatalf("refresh errors: newer=%v older=%v", newerErr, olderErr)
	}
	checkpoint, err := e.store.Metrics().Checkpoint(t.Context(), c.ID)
	if err != nil || checkpoint.Counters["post"] != 10 || !checkpoint.ObservedAt.Equal(newerAt) {
		t.Fatalf("stale sample replaced checkpoint: %+v %v", checkpoint, err)
	}
	list, err := e.store.Metrics().Recent(t.Context(), c.ID, 1)
	if err != nil || len(list) != 1 || list[0].NewViews == nil || *list[0].NewViews != 5 || !list[0].ObservedAt.Equal(newerAt) {
		t.Fatalf("stale sample replaced snapshot: %+v %v", list, err)
	}
}
