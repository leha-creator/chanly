package postgres

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// Metrics — суточные снимки метрик (R-19).
type Metrics struct{ s *Store }

func (r *Metrics) Upsert(ctx context.Context, channelID uuid.UUID, m domain.MetricsSnapshot, now time.Time) error {
	const op = "Channel.Metrics.Upsert"
	flags := m.AnomalyFlags
	if flags == nil {
		flags = []string{}
	}
	if err := r.s.q(ctx).UpsertMetrics(ctx, sqlc.UpsertMetricsParams{
		ID: ids.New(), ChannelID: channelID, Date: date(m.Date), SubscribersCount: int32(m.SubscribersCount),
		AvgViews: int32Ptr(m.AvgViews), EngagementBp: int32Ptr(m.EngagementBP), PostsPerWeek: m.PostsPerWeek,
		AnomalyFlags: flags, Now: now,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "channel_id", channelID, "date", m.Date.Format(time.DateOnly))
	return nil
}

func (r *Metrics) LastBefore(ctx context.Context, channelID uuid.UUID, day time.Time) (*domain.MetricsSnapshot, error) {
	const op = "Channel.Metrics.LastBefore"
	row, err := r.s.q(ctx).LastMetricsBefore(ctx, sqlc.LastMetricsBeforeParams{ChannelID: channelID, Date: date(day)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, r.s.fail(ctx, op, err)
	}
	s := snapshot(row.Date, row.SubscribersCount, row.AvgViews, row.EngagementBp, row.PostsPerWeek, row.AnomalyFlags)
	return &s, nil
}

func (r *Metrics) Recent(ctx context.Context, channelID uuid.UUID, n int) ([]domain.MetricsSnapshot, error) {
	const op = "Channel.Metrics.Recent"
	rows, err := r.s.q(ctx).RecentMetrics(ctx, sqlc.RecentMetricsParams{ChannelID: channelID, Lim: int32(n)})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.MetricsSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, snapshot(row.Date, row.SubscribersCount, row.AvgViews, row.EngagementBp, row.PostsPerWeek, row.AnomalyFlags))
	}
	slices.Reverse(out) // старые первыми
	r.s.debug(ctx, op, "channel_id", channelID, "count", len(out))
	return out, nil
}

func snapshot(d pgtype.Date, subs int32, avg, eng *int32, ppw pgtype.Numeric, flags []string) domain.MetricsSnapshot {
	s := domain.MetricsSnapshot{
		Date: d.Time, SubscribersCount: int(subs), AvgViews: intPtr(avg), EngagementBP: intPtr(eng), AnomalyFlags: flags,
	}
	if s.AnomalyFlags == nil {
		s.AnomalyFlags = []string{}
	}
	if ppw.Valid {
		if f, err := ppw.Float64Value(); err == nil && f.Valid {
			v := f.Float64
			s.PostsPerWeek = &v
		}
	}
	return s
}

func date(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	x := int32(*v)
	return &x
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	x := int(*v)
	return &x
}
