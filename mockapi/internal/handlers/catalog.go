package handlers

import (
	"context"
	"slices"
	"strings"
	"time"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func catalogPrices(tx *store.Tx, c *store.Channel) []api.CatalogPrice {
	out := []api.CatalogPrice{}
	for _, f := range tx.D.Formats {
		if p := c.Prices[f.ID]; p != nil && p.IsEnabled {
			_, total := store.TotalFor(p.Amount)
			out = append(out, api.CatalogPrice{FormatId: f.ID, TotalAmount: total})
		}
	}
	return out
}

func catalogChannel(tx *store.Tx, orgID api.Uuid, c *store.Channel) api.CatalogChannel {
	cat := api.ChannelCategory{}
	if c.CategoryID != nil {
		if x := tx.Category(*c.CategoryID); x != nil {
			cat = api.ChannelCategory{Id: x.ID, Slug: x.Slug, Name: x.Name}
		}
	}
	inCart := slices.ContainsFunc(tx.D.CartItems, func(it *store.CartItem) bool { return it.OrgID == orgID && it.ChannelID == c.ID })
	out := api.CatalogChannel{
		Id: c.ID, Title: c.Title, IconUrl: strPtr(c.IconURL), Link: c.Link, Description: strPtr(c.Description),
		Category: cat, SubscribersCount: c.Subscribers, AvgViews: ptr(c.AvgViews), Rating: c.Rating,
		RknNumber: strPtr(c.RknNumber), Prices: catalogPrices(tx, c), InCart: &inCart,
	}
	if c.Subscribers > 0 {
		out.EngagementBp = ptr(c.AvgViews * 10000 / c.Subscribers)
	}
	if len(c.AnomalyFlags) > 0 {
		flags := []api.CatalogChannelAnomalyFlags{}
		for _, f := range c.AnomalyFlags {
			flags = append(flags, api.CatalogChannelAnomalyFlags(f))
		}
		out.AnomalyFlags = &flags
	}
	return out
}

// priceFor — итог для рекламодателя по выбранному формату или минимальный по каналу.
func priceFor(c *store.Channel, format *string) (int64, bool) {
	best, ok := int64(0), false
	for id, p := range c.Prices {
		if !p.IsEnabled || (format != nil && id != *format) {
			continue
		}
		_, t := store.TotalFor(p.Amount)
		if !ok || t < best {
			best, ok = t, true
		}
	}
	return best, ok
}

// cpv — цена за просмотр в копейках с округлением вниз; нет просмотров — false.
func cpv(c *store.Channel, format *string) (int64, bool) {
	price, ok := priceFor(c, format)
	if !ok || c.AvgViews <= 0 {
		return 0, false
	}
	return price / int64(c.AvgViews), true
}

func (h *Handlers) ListCatalogChannels(ctx context.Context, req api.ListCatalogChannelsRequestObject) (api.ListCatalogChannelsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	p := req.Params
	var found []*store.Channel
	for _, c := range tx.D.Channels {
		if c.Status != "active" {
			continue
		}
		if p.Q != nil {
			q := strings.ToLower(*p.Q)
			if !strings.Contains(strings.ToLower(c.Title), q) && !strings.Contains(strings.ToLower(c.Description), q) {
				continue
			}
		}
		if p.CategoryId != nil && (c.CategoryID == nil || *c.CategoryID != *p.CategoryId) {
			continue
		}
		price, has := priceFor(c, p.FormatId)
		if !has || (p.PriceFrom != nil && price < *p.PriceFrom) || (p.PriceTo != nil && price > *p.PriceTo) {
			continue
		}
		if (p.SubscribersFrom != nil && c.Subscribers < *p.SubscribersFrom) || (p.SubscribersTo != nil && c.Subscribers > *p.SubscribersTo) {
			continue
		}
		found = append(found, c)
	}
	sortBy := api.SubscribersDesc
	if p.Sort != nil {
		sortBy = *p.Sort
	}
	slices.SortStableFunc(found, func(a, b *store.Channel) int {
		pa, _ := priceFor(a, p.FormatId)
		pb, _ := priceFor(b, p.FormatId)
		switch sortBy {
		case api.PriceAsc:
			return int(pa - pb)
		case api.PriceDesc:
			return int(pb - pa)
		case api.EngagementDesc:
			return b.AvgViews*10000/max(b.Subscribers, 1) - a.AvgViews*10000/max(a.Subscribers, 1)
		case api.RatingDesc:
			return b.Rating - a.Rating
		case api.AvgViewsDesc:
			return b.AvgViews - a.AvgViews
		case api.CpvAsc:
			// Каналы без просмотров — в конце, как в бэкенде.
			ca, oka := cpv(a, p.FormatId)
			cb, okb := cpv(b, p.FormatId)
			switch {
			case oka && okb:
				return int(ca - cb)
			case oka:
				return -1
			case okb:
				return 1
			}
			return 0
		default:
			return b.Subscribers - a.Subscribers
		}
	})
	h.logger(ctx).Debug("catalog filtered", "op", "Catalog.List", "q", p.Q, "category_id", p.CategoryId,
		"format_id", p.FormatId, "sort", sortBy, "found", len(found))
	pg, next, total := page(found, p.Limit, p.Cursor)
	items := []api.CatalogChannel{}
	for _, c := range pg {
		it := catalogChannel(tx, o.ID, c)
		if v, ok := cpv(c, p.FormatId); ok {
			it.Cpv = &v
		}
		items = append(items, it)
	}
	return api.ListCatalogChannels200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetCatalogChannel(ctx context.Context, req api.GetCatalogChannelRequestObject) (api.GetCatalogChannelResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.Channel(req.Id)
	if err != nil || c.Status != "active" {
		return nil, store.NotFound("channel", req.Id)
	}
	b := catalogChannel(tx, o.ID, c)
	out := api.GetCatalogChannel200JSONResponse{
		Id: b.Id, Title: b.Title, IconUrl: b.IconUrl, Link: b.Link, Description: b.Description, Category: b.Category,
		SubscribersCount: b.SubscribersCount, AvgViews: b.AvgViews, EngagementBp: b.EngagementBp, Rating: b.Rating,
		RknNumber: b.RknNumber, Prices: b.Prices, InCart: b.InCart, MaxDescription: strPtr(c.MaxDescription),
		PostsPerWeek: ptr(c.PostsPerWeek), MetricsHistory: []api.ChannelMetricsPoint{}, Unavailability: unavailability(c),
	}
	if b.AnomalyFlags != nil {
		flags := []api.CatalogChannelDetailAnomalyFlags{}
		for _, f := range *b.AnomalyFlags {
			flags = append(flags, api.CatalogChannelDetailAnomalyFlags(f))
		}
		out.AnomalyFlags = &flags
	}
	out.MetricsHistory = metricsHistory(tx, c)
	return out, nil
}

// metricsHistory returns stored observations only. New channels have no history;
// synthetic demonstration history is installed explicitly by seed.Load.
func metricsHistory(tx *store.Tx, c *store.Channel) []api.ChannelMetricsPoint {
	out := []api.ChannelMetricsPoint{}
	y, m, d := tx.Now.In(time.FixedZone("MSK", 3*60*60)).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	observations := slices.Clone(c.MetricsHistory)
	slices.SortFunc(observations, func(a, b api.ChannelMetricsPoint) int { return a.Date.Time.Compare(b.Date.Time) })
	for i, point := range observations {
		point.SubscribersChange = nil
		if i > 0 && observations[i-1].Date.Time.AddDate(0, 0, 1).Equal(point.Date.Time) {
			change := int64(point.SubscribersCount) - int64(observations[i-1].SubscribersCount)
			point.SubscribersChange = &change
		}
		if !point.Date.Time.Before(today.AddDate(0, 0, -29)) && !point.Date.Time.After(today) {
			out = append(out, point)
		}
	}
	return out
}

func unavailability(c *store.Channel) []api.Unavailability {
	out := []api.Unavailability{}
	for _, u := range c.Unavailability {
		out = append(out, api.Unavailability{Id: u.ID, StartsAt: u.StartsAt, EndsAt: u.EndsAt})
	}
	return out
}

func (h *Handlers) ListChannelCategories(ctx context.Context, req api.ListChannelCategoriesRequestObject) (api.ListChannelCategoriesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	items := []api.ChannelCategory{}
	for _, c := range tx.D.Categories {
		if c.Archived {
			continue
		}
		items = append(items, api.ChannelCategory{Id: c.ID, Slug: c.Slug, Name: c.Name})
	}
	return api.ListChannelCategories200JSONResponse{Items: items}, nil
}

func (h *Handlers) ListPlacementFormats(ctx context.Context, req api.ListPlacementFormatsRequestObject) (api.ListPlacementFormatsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	items := []api.PlacementFormat{}
	for _, f := range tx.D.Formats {
		items = append(items, api.PlacementFormat{Id: f.ID, Name: f.Name, TopHours: f.TopHours, DurationHours: f.DurationHours})
	}
	return api.ListPlacementFormats200JSONResponse{Items: items}, nil
}

// ---------------------------------------------------------------- корзина

func cart(tx *store.Tx, orgID api.Uuid) api.Cart {
	out := api.Cart{Items: []api.CartItem{}}
	for _, l := range tx.Cart(orgID) {
		it := api.CartItem{Id: l.Item.ID, FormatId: l.Item.FormatID, ShownTotalAmount: l.Item.ShownTotal,
			CurrentTotalAmount: l.CurrentTotal, Available: l.Available}
		if l.Channel != nil {
			it.Channel = channelRef(l.Channel)
		}
		it.PriceChanged = l.CurrentTotal != nil && *l.CurrentTotal != l.Item.ShownTotal
		if l.CurrentTotal != nil {
			out.TotalAmount += *l.CurrentTotal
		}
		out.Items = append(out.Items, it)
	}
	return out
}

func (h *Handlers) GetCart(ctx context.Context, req api.GetCartRequestObject) (api.GetCartResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetCart200JSONResponse(cart(tx, o.ID)), nil
}

func enabledTotal(c *store.Channel, format string) (int64, error) {
	p := c.Prices[format]
	if p == nil || !p.IsEnabled {
		return 0, store.Validation("format_id", "format is not available for this channel")
	}
	_, total := store.TotalFor(p.Amount)
	return total, nil
}

func (h *Handlers) AddCartItem(ctx context.Context, req api.AddCartItemRequestObject) (api.AddCartItemResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.Channel(req.Body.ChannelId)
	if err != nil || c.Status != "active" {
		return nil, store.NotFound("channel", req.Body.ChannelId)
	}
	total, err := enabledTotal(c, req.Body.FormatId)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(tx.D.CartItems, func(it *store.CartItem) bool { return it.OrgID == o.ID && it.ChannelID == c.ID }) {
		return nil, store.Conflict("conflict", "channel is already in cart", map[string]any{"channel_id": c.ID})
	}
	tx.D.CartItems = append(tx.D.CartItems, &store.CartItem{ID: store.NewID(), OrgID: o.ID, ChannelID: c.ID,
		FormatID: req.Body.FormatId, ShownTotal: total, CreatedAt: tx.Now})
	h.logger(ctx).Info("cart item added", "op", "Cart.Add", "organization_id", o.ID, "channel_id", c.ID, "format_id", req.Body.FormatId)
	return api.AddCartItem201JSONResponse(cart(tx, o.ID)), nil
}

func (h *Handlers) cartItem(tx *store.Tx, orgID, id api.Uuid) (*store.CartItem, error) {
	for _, it := range tx.D.CartItems {
		if it.ID == id && it.OrgID == orgID {
			return it, nil
		}
	}
	return nil, store.NotFound("cart_item", id)
}

func (h *Handlers) UpdateCartItem(ctx context.Context, req api.UpdateCartItemRequestObject) (api.UpdateCartItemResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	it, err := h.cartItem(tx, o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	c, err := tx.Channel(it.ChannelID)
	if err != nil {
		return nil, err
	}
	total, err := enabledTotal(c, req.Body.FormatId)
	if err != nil {
		return nil, err
	}
	it.FormatID, it.ShownTotal = req.Body.FormatId, total
	h.logger(ctx).Info("cart item updated", "op", "Cart.Update", "organization_id", o.ID, "channel_id", c.ID, "format_id", it.FormatID)
	return api.UpdateCartItem200JSONResponse(cart(tx, o.ID)), nil
}

func (h *Handlers) DeleteCartItem(ctx context.Context, req api.DeleteCartItemRequestObject) (api.DeleteCartItemResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := h.cartItem(tx, o.ID, req.Id); err != nil {
		return nil, err
	}
	tx.D.CartItems = slices.DeleteFunc(tx.D.CartItems, func(it *store.CartItem) bool { return it.ID == req.Id })
	h.logger(ctx).Info("cart item removed", "op", "Cart.Remove", "organization_id", o.ID, "cart_item_id", req.Id)
	return api.DeleteCartItem200JSONResponse(cart(tx, o.ID)), nil
}
