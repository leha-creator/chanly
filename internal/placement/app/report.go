package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// OrderReport — отчёт по заказу своей организации (S-09, R-14 этапа «Уведомления и отчёт по
// кампании»): строки по размещениям с просмотрами, ссылкой на пост и суммами, итог с CPV,
// ссылки текущей версии поста с UTM-метками. Чужой заказ — not_found.
func (s *Service) OrderReport(ctx context.Context, orgID, orderID uuid.UUID) (domain.Report, error) {
	const op = "Order.Report"
	o, err := s.Orders.Get(ctx, orgID, orderID)
	if err != nil {
		return domain.Report{}, notFound(err, "order")
	}
	c, err := s.Campaigns.Get(ctx, o.OrganizationID, o.CampaignID)
	if err != nil {
		return domain.Report{}, err
	}
	v, err := s.Versions.Get(ctx, o.CurrentPostVersionID)
	if err != nil {
		return domain.Report{}, err
	}
	ps, err := s.Placements.ListByOrder(ctx, o.ID)
	if err != nil {
		return domain.Report{}, err
	}
	ids := make([]uuid.UUID, 0, len(ps))
	channels := make([]uuid.UUID, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
		channels = append(channels, p.ChannelID)
	}
	offers, err := s.Channels.Offers(ctx, channels)
	if err != nil {
		return domain.Report{}, err
	}
	held, err := s.Holds.HasActive(ctx, ids)
	if err != nil {
		return domain.Report{}, err
	}
	pubs, _, err := s.control(ctx, ids)
	if err != nil {
		return domain.Report{}, err
	}
	in := make([]domain.ReportInput, 0, len(ps))
	for _, p := range ps {
		ref := offers[p.ChannelID].Ref
		if ref.ID == uuid.Nil {
			ref.ID = p.ChannelID
		}
		x := domain.ReportInput{Placement: p, Channel: ref, HoldActive: held[p.ID]}
		if pub, ok := pubs[p.ID]; ok {
			x.Publication = &pub
		}
		in = append(in, x)
	}
	r := domain.BuildReport(o, c.Name, v.Links, v.UTMSource, v.UTMMedium, in)
	s.log.DebugContext(ctx, "order report built", logx.KeyOp, op, KeyOrderID, o.ID, "rows", len(r.Rows),
		"views", r.Totals.Views, "spent_amount", int64(r.Totals.Spent))
	return r, nil
}
