package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
)

// Unavailabilities — интервалы недоступности каналов (S-13).
type Unavailabilities struct{ s *Store }

func (r *Unavailabilities) ListLive(ctx context.Context, channelID uuid.UUID, now time.Time) ([]domain.Unavailability, error) {
	const op = "Channel.Unavailabilities.ListLive"
	rows, err := r.s.q(ctx).ListLiveUnavailability(ctx, sqlc.ListLiveUnavailabilityParams{ChannelID: channelID, Now: now})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "channel_id", channelID, "count", len(rows))
	out := make([]domain.Unavailability, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Unavailability{ID: row.ID, StartsAt: row.StartsAt, EndsAt: row.EndsAt})
	}
	return out, nil
}

func (r *Unavailabilities) Create(ctx context.Context, channelID uuid.UUID, u domain.Unavailability, now time.Time) error {
	const op = "Channel.Unavailabilities.Create"
	r.s.debug(ctx, op, "channel_id", channelID, "unavailability_id", u.ID)
	if err := r.s.q(ctx).InsertUnavailability(ctx, sqlc.InsertUnavailabilityParams{
		ID: u.ID, ChannelID: channelID, StartsAt: u.StartsAt, EndsAt: u.EndsAt, CreatedAt: now,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Unavailabilities) Delete(ctx context.Context, channelID, id uuid.UUID) error {
	const op = "Channel.Unavailabilities.Delete"
	n, err := r.s.q(ctx).DeleteUnavailability(ctx, sqlc.DeleteUnavailabilityParams{ID: id, ChannelID: channelID})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "channel_id", channelID, "unavailability_id", id, "deleted", n)
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteEndedUnavailability удаляет интервалы, закончившиеся раньше before (задание cleanup, R-6).
func (s *Store) DeleteEndedUnavailability(ctx context.Context, before time.Time, batch int32) (int64, error) {
	s.debug(ctx, "Channel.DeleteEndedUnavailability", "before", before)
	return s.q(ctx).DeleteEndedUnavailability(ctx, sqlc.DeleteEndedUnavailabilityParams{Before: before, Batch: batch})
}
