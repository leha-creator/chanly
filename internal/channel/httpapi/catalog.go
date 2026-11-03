package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// cabinetAdvertiser — каталог видит пользователь с открытым кабинетом рекламодателя (R-12).
const cabinetAdvertiser = "advertiser"

func (h *Handlers) ListPlacementFormats(ctx context.Context, _ apigen.ListPlacementFormatsRequestObject) (apigen.ListPlacementFormatsResponseObject, error) {
	if !httpx.Authenticated(ctx) {
		return nil, apperr.Unauthorized("session is missing")
	}
	list, err := h.svc.Formats(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.PlacementFormat, 0, len(list))
	for _, f := range list {
		items = append(items, apigen.PlacementFormat{Id: f.ID, Name: f.Name, TopHours: f.TopHours, DurationHours: f.DurationHours})
	}
	return apigen.ListPlacementFormats200JSONResponse{Items: items}, nil
}

func (h *Handlers) ListCatalogChannels(ctx context.Context, req apigen.ListCatalogChannelsRequestObject) (apigen.ListCatalogChannelsResponseObject, error) {
	principal, err := httpx.RequireCabinet(ctx, cabinetAdvertiser)
	if err != nil {
		return nil, err
	}
	p := req.Params
	r := app.CatalogRequest{Filter: domain.CatalogFilter{
		CategoryID: p.CategoryId, SubscribersFrom: p.SubscribersFrom, SubscribersTo: p.SubscribersTo,
		PriceFrom: moneyPtr(p.PriceFrom), PriceTo: moneyPtr(p.PriceTo),
	}}
	if p.Q != nil {
		r.Filter.Query = *p.Q
	}
	if p.FormatId != nil {
		r.Filter.FormatID = *p.FormatId
	}
	if p.Sort != nil {
		r.Sort = domain.CatalogSort(*p.Sort)
	}
	if p.Limit != nil {
		r.Limit = *p.Limit
	}
	if p.Cursor != nil {
		r.Cursor = *p.Cursor
	}
	page, err := h.svc.ListCatalog(ctx, r)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(page.Items))
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	inCart := h.svc.InCart(ctx, principal.OrganizationID, ids)
	items := make([]apigen.CatalogChannel, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, catalogChannel(it, inCart[it.ID]))
	}
	return apigen.ListCatalogChannels200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) GetCatalogChannel(ctx context.Context, req apigen.GetCatalogChannelRequestObject) (apigen.GetCatalogChannelResponseObject, error) {
	principal, err := httpx.RequireCabinet(ctx, cabinetAdvertiser)
	if err != nil {
		return nil, err
	}
	card, err := h.svc.CatalogChannel(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	inCart := h.svc.InCart(ctx, principal.OrganizationID, []uuid.UUID{card.ID})
	return apigen.GetCatalogChannel200JSONResponse(catalogDetail(card, inCart[card.ID])), nil
}

func (h *Handlers) SetBloggerChannelPrices(ctx context.Context, req apigen.SetBloggerChannelPricesRequestObject) (apigen.SetBloggerChannelPricesResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	prices := make([]domain.Price, 0, len(req.Body.Prices))
	for _, p := range req.Body.Prices {
		prices = append(prices, domain.Price{FormatID: p.FormatId, Amount: money.Kopecks(p.Amount), IsEnabled: p.IsEnabled})
	}
	c, err := h.svc.SetPrices(ctx, org, req.Id, prices)
	if err != nil {
		return nil, err
	}
	v, err := h.view(ctx, c)
	if err != nil {
		return nil, err
	}
	return apigen.SetBloggerChannelPrices200JSONResponse(v), nil
}

func (h *Handlers) AddChannelUnavailability(ctx context.Context, req apigen.AddChannelUnavailabilityRequestObject) (apigen.AddChannelUnavailabilityResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.AddUnavailability(ctx, org, req.Id, domain.Unavailability{StartsAt: req.Body.StartsAt.UTC(), EndsAt: req.Body.EndsAt.UTC()})
	if err != nil {
		return nil, err
	}
	v, err := h.view(ctx, c)
	if err != nil {
		return nil, err
	}
	return apigen.AddChannelUnavailability201JSONResponse(v), nil
}

func (h *Handlers) DeleteChannelUnavailability(ctx context.Context, req apigen.DeleteChannelUnavailabilityRequestObject) (apigen.DeleteChannelUnavailabilityResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.DeleteUnavailability(ctx, org, req.Id, req.UnavailabilityId)
	if err != nil {
		return nil, err
	}
	v, err := h.view(ctx, c)
	if err != nil {
		return nil, err
	}
	return apigen.DeleteChannelUnavailability200JSONResponse(v), nil
}

func moneyPtr(v *apigen.Money) *money.Kopecks {
	if v == nil {
		return nil
	}
	k := money.Kopecks(*v)
	return &k
}
