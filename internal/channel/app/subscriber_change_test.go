package app_test

import (
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
)

func TestSubscriberChangeUsesConsecutiveDaysAndSameDayBaseline(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000921)
	p := app.MetricsPayload{ChannelID: c.ID, Date: domain.MetricsDate(start).Format(time.DateOnly)}
	refresh := func(after time.Duration, subscribers int) {
		t.Helper()
		e.clk.Advance(after)
		info := publicCh
		info.SubscribersCount = subscribers
		e.max.set(c.MaxChatID, info, allRights)
		if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	assertHistory := func(want ...*int64) {
		t.Helper()
		card, err := e.svc.CatalogChannel(t.Context(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		admin, err := e.svc.AdminGet(t.Context(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for name, history := range map[string][]domain.MetricsSnapshot{"catalog": card.History, "admin": admin.History} {
			if len(history) != len(want) {
				t.Fatalf("%s history has %d observations, want %d", name, len(history), len(want))
			}
			for i, expected := range want {
				actual := history[i].SubscribersChange
				if (actual == nil) != (expected == nil) || (actual != nil && *actual != *expected) {
					t.Fatalf("%s point %d: subscribers_change=%v want=%v", name, i, actual, expected)
				}
			}
		}
	}
	n := func(v int64) *int64 { return &v }
	assertHistory(nil)
	refresh(24*time.Hour, 4205)
	assertHistory(nil, n(5))
	// Same-day refresh remains relative to yesterday's 4200, not today's 4205.
	refresh(time.Hour, 4208)
	assertHistory(nil, n(8))
	refresh(23*time.Hour, 4208)
	assertHistory(nil, n(8), n(0))
	refresh(24*time.Hour, 4201)
	assertHistory(nil, n(8), n(0), n(-7))
	refresh(48*time.Hour, 4210)
	assertHistory(nil, n(8), n(0), n(-7), nil)
}

func TestSubscriberChangeRetainsBaselineOutsideThirtyDayWindow(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000922)
	day := domain.MetricsDate(e.clk.Now())
	for i := 0; i < 31; i++ {
		m := domain.MetricsSnapshot{Date: day.AddDate(0, 0, i-30), SubscribersCount: 100 + i}
		if err := e.store.Metrics().Upsert(t.Context(), c.ID, m, e.clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	card, err := e.svc.CatalogChannel(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := e.svc.AdminGet(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, history := range map[string][]domain.MetricsSnapshot{"catalog": card.History, "admin": admin.History} {
		if len(history) != 30 || !history[0].Date.Equal(day.AddDate(0, 0, -29)) {
			t.Fatalf("%s wrong visible dates: %+v", name, history)
		}
		for i, m := range history {
			if m.SubscribersCount != 101+i || m.SubscribersChange == nil || *m.SubscribersChange != 1 {
				t.Fatalf("%s point %d lost actual preceding-day baseline: %+v", name, i, m)
			}
		}
	}
}
