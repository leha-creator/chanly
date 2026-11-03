package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

// PlacementView — размещение для кабинета рекламодателя: канал, история, действия (R-22).
type PlacementView struct {
	Placement domain.Placement
	Channel   domain.ChannelRef
	History   []domain.HistoryEntry
	Actions   []string
	// Publication и Dispute — пост в канале и спор, если есть (R-18 этапа «Автопостинг и контроль»).
	Publication *domain.Publication
	Dispute     *domain.Dispute
	// Claim — претензия рекламодателя, если подана (R-15 этапа приёмки).
	Claim *domain.Claim
}

// OrderSummary — строка списка заказов: название кампании и число размещений по состояниям.
type OrderSummary struct {
	Order        domain.Order
	CampaignName string
	Counts       map[domain.Status]int
}

// OrderView — заказ с текущей версией поста и размещениями.
type OrderView struct {
	OrderSummary
	Version    domain.VersionInfo
	Placements []PlacementView
}

// OrderPage — страница заказов; NextCursor пуст на последней странице.
type OrderPage struct {
	Items      []OrderSummary
	NextCursor string
}

// ListOrders — заказы организации, новые сверху, с числом размещений по состояниям.
func (s *Service) ListOrders(ctx context.Context, orgID uuid.UUID, status *domain.OrderStatus, cursorStr string, limit int) (OrderPage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return OrderPage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.Orders.List(ctx, orgID, status, after, limit+1)
	if err != nil {
		return OrderPage{}, err
	}
	page := OrderPage{}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, o := range list {
		ids = append(ids, o.ID)
	}
	counts, err := s.Placements.CountByOrders(ctx, ids)
	if err != nil {
		return OrderPage{}, err
	}
	page.Items = make([]OrderSummary, 0, len(list))
	for _, o := range list {
		c, err := s.Campaigns.Get(ctx, orgID, o.CampaignID)
		if err != nil {
			return OrderPage{}, err
		}
		page.Items = append(page.Items, OrderSummary{Order: o, CampaignName: c.Name, Counts: orEmpty(counts[o.ID])})
	}
	return page, nil
}

// GetOrder — заказ организации с размещениями, историей и текущей версией поста; чужой — 404.
func (s *Service) GetOrder(ctx context.Context, orgID, id uuid.UUID) (OrderView, error) {
	o, err := s.Orders.Get(ctx, orgID, id)
	if err != nil {
		return OrderView{}, notFound(err, "order")
	}
	return s.orderView(ctx, o)
}

func (s *Service) orderView(ctx context.Context, o domain.Order) (OrderView, error) {
	c, err := s.Campaigns.Get(ctx, o.OrganizationID, o.CampaignID)
	if err != nil {
		return OrderView{}, err
	}
	v, err := s.Versions.Get(ctx, o.CurrentPostVersionID)
	if err != nil {
		return OrderView{}, err
	}
	ps, err := s.Placements.ListByOrder(ctx, o.ID)
	if err != nil {
		return OrderView{}, err
	}
	views, err := s.placementViews(ctx, ps)
	if err != nil {
		return OrderView{}, err
	}
	counts := map[domain.Status]int{}
	for _, p := range ps {
		counts[p.Status]++
	}
	return OrderView{OrderSummary: OrderSummary{Order: o, CampaignName: c.Name, Counts: counts}, Version: v, Placements: views}, nil
}

// placementViews — размещения с каналами, историей и действиями рекламодателя.
func (s *Service) placementViews(ctx context.Context, ps []domain.Placement) ([]PlacementView, error) {
	ids := make([]uuid.UUID, 0, len(ps))
	channels := make([]uuid.UUID, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
		channels = append(channels, p.ChannelID)
	}
	history, err := s.History.ListByPlacements(ctx, ids)
	if err != nil {
		return nil, err
	}
	offers, err := s.Channels.Offers(ctx, channels)
	if err != nil {
		return nil, err
	}
	pubs, disputes, err := s.control(ctx, ids)
	if err != nil {
		return nil, err
	}
	claims := map[uuid.UUID]domain.Claim{}
	if s.Claims != nil {
		if claims, err = s.Claims.ListByPlacements(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]PlacementView, 0, len(ps))
	for _, p := range ps {
		ref := offers[p.ChannelID].Ref
		if ref.ID == uuid.Nil {
			ref.ID = p.ChannelID
		}
		h := history[p.ID]
		if h == nil {
			h = []domain.HistoryEntry{}
		}
		c, hasClaim := claims[p.ID]
		view := PlacementView{Placement: p, Channel: ref, History: h, Actions: domain.AdvertiserActions(&p, hasClaim, s.Clock.Now())}
		if hasClaim {
			view.Claim = &c
		}
		if pub, ok := pubs[p.ID]; ok {
			view.Publication = &pub
		}
		if d, ok := disputes[p.ID]; ok {
			view.Dispute = &d
		}
		out = append(out, view)
	}
	return out, nil
}

// placementView — одно размещение для ответа действия рекламодателя.
func (s *Service) placementView(ctx context.Context, p domain.Placement) (PlacementView, error) {
	views, err := s.placementViews(ctx, []domain.Placement{p})
	if err != nil {
		return PlacementView{}, err
	}
	return views[0], nil
}

func orEmpty(m map[domain.Status]int) map[domain.Status]int {
	if m == nil {
		return map[domain.Status]int{}
	}
	return m
}

// control — публикации и споры размещений одним запросом на каждую таблицу (R-18).
func (s *Service) control(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Publication, map[uuid.UUID]domain.Dispute, error) {
	if s.Publications == nil || s.Disputes == nil {
		return map[uuid.UUID]domain.Publication{}, map[uuid.UUID]domain.Dispute{}, nil
	}
	pubs, err := s.Publications.ListByPlacements(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	disputes, err := s.Disputes.ListByPlacements(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	return pubs, disputes, nil
}
