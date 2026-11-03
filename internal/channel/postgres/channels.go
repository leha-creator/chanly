package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
)

// Channels — каналы.
type Channels struct{ s *Store }

func (r *Channels) Create(ctx context.Context, c *domain.Channel) error {
	const op = "Channel.Channels.Create"
	r.s.debug(ctx, op, "channel_id", c.ID, "max_chat_id", c.MaxChatID, "status", c.Status)
	err := r.s.q(ctx).InsertChannel(ctx, sqlc.InsertChannelParams{
		ID: c.ID, OrganizationID: c.OrganizationID, MaxChatID: c.MaxChatID,
		Title: text(c.Title), MaxDescription: text(c.MaxDescription), IconUrl: text(c.IconURL), Link: text(c.Link),
		IsPublic: c.IsPublic, AddedByMaxUserID: c.AddedByMaxUserID, Description: text(c.Description),
		CategoryID: c.CategoryID, Status: string(c.Status), StatusReason: text(c.StatusReason),
		StatusBeforeDisconnect: statusPtr(c.StatusBeforeDisconnect), UnclaimedSince: c.UnclaimedSince,
		BotIsAdmin: c.BotIsAdmin, BotPermissions: perms(c.BotPermissions), BotPermissionsCheckedAt: c.BotPermissionsCheckedAt,
		SubscribersCount: int32(c.SubscribersCount), RknNumber: text(c.RknNumber), RknStatus: string(c.RknStatus),
		RknCheckedAt: c.RknCheckedAt, Rating: int16(c.Rating), ModeratedAt: c.ModeratedAt,
		LockVersion: int32(c.LockVersion), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	return nil
}

func (r *Channels) Save(ctx context.Context, c *domain.Channel) error {
	const op = "Channel.Channels.Save"
	n, err := r.s.q(ctx).SaveChannel(ctx, sqlc.SaveChannelParams{
		OrganizationID: c.OrganizationID, Title: text(c.Title), MaxDescription: text(c.MaxDescription),
		IconUrl: text(c.IconURL), Link: text(c.Link), IsPublic: c.IsPublic, AddedByMaxUserID: c.AddedByMaxUserID,
		Description: text(c.Description), CategoryID: c.CategoryID, Status: string(c.Status),
		StatusReason: text(c.StatusReason), StatusBeforeDisconnect: statusPtr(c.StatusBeforeDisconnect),
		UnclaimedSince: c.UnclaimedSince, BotIsAdmin: c.BotIsAdmin, BotPermissions: perms(c.BotPermissions),
		BotPermissionsCheckedAt: c.BotPermissionsCheckedAt, SubscribersCount: int32(c.SubscribersCount),
		RknNumber: text(c.RknNumber), RknStatus: string(c.RknStatus), RknCheckedAt: c.RknCheckedAt,
		RknCheckedBy: c.RknCheckedBy, ModeratedAt: c.ModeratedAt, ModeratedBy: c.ModeratedBy,
		LockVersion: int32(c.LockVersion + 1), UpdatedAt: c.UpdatedAt, ID: c.ID, PrevLockVersion: int32(c.LockVersion),
	})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	if n == 0 {
		r.s.debug(ctx, op, "channel_id", c.ID, "lock_version", c.LockVersion, "result", "stale")
		return domain.ErrStaleVersion
	}
	c.LockVersion++
	r.s.log.DebugContext(ctx, "channel saved", "op", op, "channel_id", c.ID, "status", c.Status, "lock_version", c.LockVersion)
	return nil
}

func (r *Channels) GetByMaxChatIDForUpdate(ctx context.Context, maxChatID int64) (*domain.Channel, error) {
	const op = "Channel.Channels.GetByMaxChatIDForUpdate"
	r.s.debug(ctx, op, "max_chat_id", maxChatID)
	row, err := r.s.q(ctx).GetChannelByMaxChatIDForUpdate(ctx, maxChatID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *Channels) GetByMaxChatID(ctx context.Context, maxChatID int64) (*domain.Channel, error) {
	const op = "Channel.Channels.GetByMaxChatID"
	r.s.debug(ctx, op, "max_chat_id", maxChatID)
	row, err := r.s.q(ctx).GetChannelByMaxChatID(ctx, maxChatID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *Channels) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Channel, error) {
	const op = "Channel.Channels.GetForUpdate"
	r.s.debug(ctx, op, "channel_id", id)
	row, err := r.s.q(ctx).GetChannelForUpdate(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *Channels) GetForOrganization(ctx context.Context, orgID, id uuid.UUID) (*domain.Channel, error) {
	const op = "Channel.Channels.GetForOrganization"
	r.s.debug(ctx, op, "organization_id", orgID, "channel_id", id)
	row, err := r.s.q(ctx).GetChannelForOrganization(ctx, sqlc.GetChannelForOrganizationParams{ID: id, OrganizationID: &orgID})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *Channels) GetForOrganizationForUpdate(ctx context.Context, orgID, id uuid.UUID) (*domain.Channel, error) {
	const op = "Channel.Channels.GetForOrganizationForUpdate"
	r.s.debug(ctx, op, "organization_id", orgID, "channel_id", id)
	row, err := r.s.q(ctx).GetChannelForOrganizationForUpdate(ctx, sqlc.GetChannelForOrganizationForUpdateParams{ID: id, OrganizationID: &orgID})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *Channels) ListByOrganization(ctx context.Context, orgID uuid.UUID) ([]*domain.Channel, error) {
	const op = "Channel.Channels.ListByOrganization"
	rows, err := r.s.q(ctx).ListChannelsByOrganization(ctx, &orgID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "organization_id", orgID, "count", len(rows))
	out := make([]*domain.Channel, 0, len(rows))
	for _, row := range rows {
		out = append(out, channel(row))
	}
	if err := r.s.loadPrices(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Channels) ListUnclaimedBefore(ctx context.Context, before time.Time, limit int) ([]uuid.UUID, error) {
	const op = "Channel.Channels.ListUnclaimedBefore"
	ids, err := r.s.q(ctx).ListUnclaimedChannelsBefore(ctx, sqlc.ListUnclaimedChannelsBeforeParams{Before: before, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "before", before, "count", len(ids))
	return ids, nil
}

func channel(row sqlc.Channel) *domain.Channel {
	c := &domain.Channel{
		ID: row.ID, OrganizationID: row.OrganizationID, MaxChatID: row.MaxChatID,
		Title: str(row.Title), MaxDescription: str(row.MaxDescription), IconURL: str(row.IconUrl), Link: str(row.Link),
		IsPublic: row.IsPublic, AddedByMaxUserID: row.AddedByMaxUserID, Description: str(row.Description),
		CategoryID: row.CategoryID, Status: domain.Status(row.Status), StatusReason: str(row.StatusReason),
		UnclaimedSince: row.UnclaimedSince, BotIsAdmin: row.BotIsAdmin, BotPermissions: perms(row.BotPermissions),
		BotPermissionsCheckedAt: row.BotPermissionsCheckedAt, SubscribersCount: int(row.SubscribersCount),
		RknNumber: str(row.RknNumber), RknStatus: domain.RknStatus(row.RknStatus), RknCheckedAt: row.RknCheckedAt,
		RknCheckedBy: row.RknCheckedBy, Rating: int(row.Rating), ModeratedAt: row.ModeratedAt, ModeratedBy: row.ModeratedBy, OrdPlatformReadyAt: row.OrdPlatformReadyAt,
		LockVersion: int(row.LockVersion),
		CreatedAt:   row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.StatusBeforeDisconnect != nil {
		s := domain.Status(*row.StatusBeforeDisconnect)
		c.StatusBeforeDisconnect = &s
	}
	return c
}

func statusPtr(s *domain.Status) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

func perms(p []string) []string {
	if p == nil {
		return []string{}
	}
	return p
}

func (r *Channels) ListForMetrics(ctx context.Context, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	const op = "Channel.Channels.ListForMetrics"
	ids, err := r.s.q(ctx).ListChannelsForMetrics(ctx, sqlc.ListChannelsForMetricsParams{After: after, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "after", after, "count", len(ids))
	return ids, nil
}

func (r *Channels) SetOrdPlatformReady(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	const op = "Channel.Channels.SetOrdPlatformReady"
	n, err := r.s.q(ctx).SetChannelOrdPlatformReady(ctx, sqlc.SetChannelOrdPlatformReadyParams{ID: id, At: &at})
	if err != nil {
		return false, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "channel_id", id, "updated", n > 0)
	return n > 0, nil
}

func (r *Channels) LowerRating(ctx context.Context, id uuid.UUID, points int) (int, int, error) {
	const op = "Channel.Channels.LowerRating"
	row, err := r.s.q(ctx).LowerChannelRating(ctx, sqlc.LowerChannelRatingParams{ID: id, Points: int32(points)})
	if err != nil {
		return 0, 0, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "channel_id", id, "from", row.FromRating, "to", row.ToRating)
	return int(row.FromRating), int(row.ToRating), nil
}
