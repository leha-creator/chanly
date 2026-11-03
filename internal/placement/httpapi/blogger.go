package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/placement/domain"
)

func (h *Handlers) ListBloggerPlacements(ctx context.Context, req apigen.ListBloggerPlacementsRequestObject) (apigen.ListBloggerPlacementsResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	var status *domain.Status
	if req.Params.Status != nil {
		s := domain.Status(*req.Params.Status)
		status = &s
	}
	page, err := h.svc.ListBloggerPlacements(ctx, org, status, req.Params.ChannelId, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.BloggerPlacement, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, toBloggerPlacement(it))
	}
	return apigen.ListBloggerPlacements200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) GetBloggerPlacement(ctx context.Context, req apigen.GetBloggerPlacementRequestObject) (apigen.GetBloggerPlacementResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetBloggerPlacement(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetBloggerPlacement200JSONResponse(toBloggerPlacement(v)), nil
}

func (h *Handlers) BloggerAcceptPlacement(ctx context.Context, req apigen.BloggerAcceptPlacementRequestObject) (apigen.BloggerAcceptPlacementResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Accept(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.BloggerAcceptPlacement200JSONResponse(toBloggerPlacement(v)), nil
}

func (h *Handlers) BloggerDeclinePlacement(ctx context.Context, req apigen.BloggerDeclinePlacementRequestObject) (apigen.BloggerDeclinePlacementResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	comment := ""
	if req.Body != nil {
		comment = deref(req.Body.Comment)
	}
	v, err := h.svc.Decline(ctx, org, user, req.Id, comment, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.BloggerDeclinePlacement200JSONResponse(toBloggerPlacement(v)), nil
}

func (h *Handlers) BloggerSchedulePlacement(ctx context.Context, req apigen.BloggerSchedulePlacementRequestObject) (apigen.BloggerSchedulePlacementResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Schedule(ctx, org, user, req.Id, domain.ScheduleMode(req.Body.Mode), req.Body.PublishAt, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.BloggerSchedulePlacement200JSONResponse(toBloggerPlacement(v)), nil
}

func (h *Handlers) BloggerReschedulePlacement(ctx context.Context, req apigen.BloggerReschedulePlacementRequestObject) (apigen.BloggerReschedulePlacementResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Reschedule(ctx, org, user, req.Id, req.Body.PublishAt, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.BloggerReschedulePlacement200JSONResponse(toBloggerPlacement(v)), nil
}
