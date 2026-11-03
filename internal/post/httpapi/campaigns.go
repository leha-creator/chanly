package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/post/domain"
)

func (h *Handlers) ListCampaigns(ctx context.Context, req apigen.ListCampaignsRequestObject) (apigen.ListCampaignsResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	var status *domain.CampaignStatus
	if req.Params.Status != nil {
		s := domain.CampaignStatus(*req.Params.Status)
		status = &s
	}
	page, err := h.svc.ListCampaigns(ctx, org, status, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Campaign, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, toCampaign(c))
	}
	return apigen.ListCampaigns200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) CreateCampaign(ctx context.Context, req apigen.CreateCampaignRequestObject) (apigen.CreateCampaignResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	in, err := fromCampaignInput(apigen.CampaignInput(*req.Body))
	if err != nil {
		return nil, err
	}
	c, err := h.svc.CreateCampaign(ctx, org, user, in)
	if err != nil {
		return nil, err
	}
	return apigen.CreateCampaign201JSONResponse(toCampaign(c)), nil
}

func (h *Handlers) GetCampaign(ctx context.Context, req apigen.GetCampaignRequestObject) (apigen.GetCampaignResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.GetCampaign(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetCampaign200JSONResponse(toCampaign(c)), nil
}

func (h *Handlers) UpdateCampaign(ctx context.Context, req apigen.UpdateCampaignRequestObject) (apigen.UpdateCampaignResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	in, err := fromCampaignInput(apigen.CampaignInput(*req.Body))
	if err != nil {
		return nil, err
	}
	c, err := h.svc.UpdateCampaign(ctx, org, user, req.Id, in)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateCampaign200JSONResponse(toCampaign(c)), nil
}
