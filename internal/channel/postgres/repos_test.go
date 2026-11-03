package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestPricesUnavailabilityMetrics(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	id := seed(t, d, spec{status: "moderation", prices: map[string]int64{"f_1_24": 300000, "f_3_72": 700000}})
	other := seed(t, d, spec{status: "moderation"})

	formats, err := st.Formats().ListActive(ctx)
	if err != nil || len(formats) != 3 || formats[0].ID != "f_1_24" || formats[2].DurationHours != 72 {
		t.Fatalf("formats %+v %v", formats, err)
	}
	// Полный набор заменяет прежний: f_3_72 удалён.
	if err := st.Prices().Replace(ctx, id, []domain.Price{{FormatID: "f_2_48", Amount: 450000, IsEnabled: true},
		{FormatID: "f_1_24", Amount: 310000}}, now); err != nil {
		t.Fatal(err)
	}
	c, err := st.Channels().GetForUpdate(ctx, id)
	if err != nil || len(c.Prices) != 2 || c.Prices[0].FormatID != "f_1_24" || c.Prices[0].Amount != 310000 ||
		c.Prices[0].IsEnabled || !c.Prices[1].Offered() {
		t.Fatalf("prices %+v %v", c.Prices, err)
	}

	u := domain.Unavailability{ID: ids.New(), StartsAt: now.Add(time.Hour), EndsAt: now.Add(48 * time.Hour)}
	past := domain.Unavailability{ID: ids.New(), StartsAt: now.Add(-72 * time.Hour), EndsAt: now.Add(-48 * time.Hour)}
	for _, x := range []domain.Unavailability{u, past} {
		if err := st.Unavailabilities().Create(ctx, id, x, now); err != nil {
			t.Fatal(err)
		}
	}
	live, err := st.Unavailabilities().ListLive(ctx, id, now)
	if err != nil || len(live) != 1 || live[0].ID != u.ID {
		t.Fatalf("live %+v %v", live, err)
	}
	if err := st.Unavailabilities().Delete(ctx, other, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete from other channel: %v", err)
	}
	if n, err := st.DeleteEndedUnavailability(ctx, now.Add(-24*time.Hour), 100); err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	if err := st.Unavailabilities().Delete(ctx, id, u.ID); err != nil {
		t.Fatal(err)
	}

	avg, ppw := 1200, 3.25
	day := domain.MetricsDate(now)
	for _, s := range []domain.MetricsSnapshot{
		{Date: day.AddDate(0, 0, -1), SubscribersCount: 900},
		{Date: day, SubscribersCount: 1000},
		{Date: day, SubscribersCount: 1100, AvgViews: &avg, PostsPerWeek: &ppw, AnomalyFlags: []string{domain.FlagSubscribersSpike}},
	} {
		if err := st.Metrics().Upsert(ctx, id, s, now); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := st.Metrics().Recent(ctx, id, 30)
	if err != nil || len(recent) != 2 || recent[0].SubscribersCount != 900 || recent[1].SubscribersCount != 1100 ||
		*recent[1].AvgViews != 1200 || *recent[1].PostsPerWeek != 3.25 || recent[1].AnomalyFlags[0] != domain.FlagSubscribersSpike {
		t.Fatalf("recent %+v %v", recent, err)
	}
	prev, err := st.Metrics().LastBefore(ctx, id, day)
	if err != nil || prev == nil || prev.SubscribersCount != 900 || prev.AvgViews != nil || prev.PostsPerWeek != nil {
		t.Fatalf("last before %+v %v", prev, err)
	}
	if none, err := st.Metrics().LastBefore(ctx, other, day); err != nil || none != nil {
		t.Fatalf("no snapshot: %+v %v", none, err)
	}
}

func TestAdminChannels(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	mod := seed(t, d, spec{status: "moderation"})
	act := seed(t, d, spec{subs: 20000, rknStatus: "confirmed", prices: price(100000)})
	if _, err := d.Pool.Exec(ctx, `UPDATE channel SET rkn_checked_at = $2, updated_at = updated_at + interval '1 second' WHERE id = $1`,
		act, now.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, err := st.AdminChannels().ListForAdmin(ctx, domain.AdminFilter{Limit: 10, RecheckBefore: now.Add(-90 * 24 * time.Hour)})
	if err != nil || len(all) != 2 || all[0].Channel.ID != mod || all[0].Organization == nil || all[0].Organization.INN == "" {
		t.Fatalf("all %+v %v", all, err)
	}
	status := domain.StatusModeration
	due := true
	for name, tc := range map[string]struct {
		f    domain.AdminFilter
		want int
	}{
		"status":      {domain.AdminFilter{Status: &status}, 1},
		"recheck due": {domain.AdminFilter{RknRecheckDue: &due}, 1},
		"after first": {domain.AdminFilter{After: &domain.AdminCursor{UpdatedAt: all[0].Channel.UpdatedAt, ID: mod}}, 1},
	} {
		tc.f.Limit, tc.f.RecheckBefore = 10, now.Add(-90*24*time.Hour)
		got, err := st.AdminChannels().ListForAdmin(ctx, tc.f)
		if err != nil || len(got) != tc.want {
			t.Errorf("%s: %d rows, err %v", name, len(got), err)
		}
	}
	got, err := st.AdminChannels().GetForAdmin(ctx, act)
	if err != nil || len(got.Channel.Prices) != 1 || got.Organization.Name == "" {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO audit_log (id, actor_type, action, object_type, object_id, reason, data, created_at)
		VALUES ($1, 'admin', 'channel.rejected', 'channel', $2, 'нет тематики', '{}', now()),
		       ($3, 'admin', 'channel.rkn_checked', 'channel', $2, NULL, '{"result":"confirmed"}', now() + interval '1 second'),
		       ($4, 'blogger', 'channel.updated', 'channel', $2, NULL, '{}', now())`,
		ids.New(), mod.String(), ids.New(), ids.New()); err != nil {
		t.Fatal(err)
	}
	h, err := st.AdminChannels().ModerationHistoryForAdmin(ctx, mod)
	if err != nil || len(h) != 2 || h[0].Action != "rejected" || h[0].Comment != "нет тематики" || h[1].Result != "confirmed" {
		t.Fatalf("history %+v %v", h, err)
	}
}

func TestLowerRating(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	id := seed(t, d, spec{status: "moderation"})
	from, to, err := st.Channels().LowerRating(ctx, id, 10)
	if err != nil || from != 100 || to != 90 {
		t.Fatalf("lower: %d %d %v", from, to, err)
	}
	// Не ниже 0.
	if from, to, err = st.Channels().LowerRating(ctx, id, 95); err != nil || from != 90 || to != 0 {
		t.Fatalf("floor: %d %d %v", from, to, err)
	}
	if _, _, err := st.Channels().LowerRating(ctx, ids.New(), 5); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
}
