package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
)

// AdminChannels — выборки администратора. Методы ...ForAdmin вызываются только после
// проверки роли (07 §4); организация в них не ограничивает выборку.
type AdminChannels struct{ s *Store }

func (r *AdminChannels) ListForAdmin(ctx context.Context, f domain.AdminFilter) ([]domain.AdminChannel, error) {
	const op = "Channel.AdminChannels.ListForAdmin"
	p := sqlc.ListChannelsForAdminParams{RecheckDue: f.RknRecheckDue, RecheckBefore: f.RecheckBefore, Lim: int32(f.Limit)}
	if f.Status != nil {
		v := string(*f.Status)
		p.Status = &v
	}
	if f.RknStatus != nil {
		v := string(*f.RknStatus)
		p.RknStatus = &v
	}
	if f.After != nil {
		p.AfterUpdatedAt, p.AfterID = &f.After.UpdatedAt, f.After.ID
	}
	rows, err := r.s.q(ctx).ListChannelsForAdmin(ctx, p)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.AdminChannel, 0, len(rows))
	chans := make([]*domain.Channel, 0, len(rows))
	for _, row := range rows {
		c := channel(row.Channel)
		chans = append(chans, c)
		out = append(out, domain.AdminChannel{Channel: c, Organization: orgRef(c.OrganizationID, row.OrganizationName, row.OrganizationInn)})
	}
	if err := r.s.loadPrices(ctx, chans...); err != nil {
		return nil, err
	}
	r.s.debug(ctx, op, "count", len(out))
	return out, nil
}

func (r *AdminChannels) GetForAdmin(ctx context.Context, id uuid.UUID) (*domain.AdminChannel, error) {
	const op = "Channel.AdminChannels.GetForAdmin"
	r.s.debug(ctx, op, "channel_id", id)
	row, err := r.s.q(ctx).GetChannelForAdmin(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	c, err := r.s.withPrices(ctx, channel(row.Channel))
	if err != nil {
		return nil, err
	}
	return &domain.AdminChannel{Channel: c, Organization: orgRef(c.OrganizationID, row.OrganizationName, row.OrganizationInn)}, nil
}

func (r *AdminChannels) GetForAdminForUpdate(ctx context.Context, id uuid.UUID) (*domain.Channel, error) {
	const op = "Channel.AdminChannels.GetForAdminForUpdate"
	r.s.debug(ctx, op, "channel_id", id)
	row, err := r.s.q(ctx).GetChannelForAdminForUpdate(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.s.withPrices(ctx, channel(row))
}

func (r *AdminChannels) ModerationHistoryForAdmin(ctx context.Context, channelID uuid.UUID) ([]domain.ModerationEntry, error) {
	const op = "Channel.AdminChannels.ModerationHistoryForAdmin"
	rows, err := r.s.q(ctx).ListModerationHistory(ctx, channelID.String())
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.ModerationEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.ModerationEntry{
			Action: strings.TrimPrefix(row.Action, "channel."), ActorType: row.ActorType, ActorUserID: row.ActorUserID,
			Result: row.Result, Comment: str(row.Reason), At: row.CreatedAt,
		})
	}
	r.s.debug(ctx, op, "channel_id", channelID, "count", len(out))
	return out, nil
}

func orgRef(id *uuid.UUID, name, inn *string) *domain.OrganizationRef {
	if id == nil {
		return nil
	}
	return &domain.OrganizationRef{ID: *id, Name: str(name), INN: str(inn)}
}
