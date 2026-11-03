package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
)

// CheckoutCampaign — оформление заказа (S-06). Операция принадлежит модулю размещений (R-1).
func (h *Handlers) CheckoutCampaign(ctx context.Context, req apigen.CheckoutCampaignRequestObject) (apigen.CheckoutCampaignResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	o, err := h.svc.Checkout(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetOrder(ctx, org, o.ID)
	if err != nil {
		return nil, err
	}
	return apigen.CheckoutCampaign201JSONResponse(toOrder(v)), nil
}

func (h *Handlers) ListOrders(ctx context.Context, req apigen.ListOrdersRequestObject) (apigen.ListOrdersResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	var status *domain.OrderStatus
	if req.Params.Status != nil {
		s := domain.OrderStatus(*req.Params.Status)
		status = &s
	}
	page, err := h.svc.ListOrders(ctx, org, status, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.OrderSummary, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, toOrderSummary(it))
	}
	return apigen.ListOrders200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) GetOrder(ctx context.Context, req apigen.GetOrderRequestObject) (apigen.GetOrderResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetOrder(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetOrder200JSONResponse(toOrder(v)), nil
}

func (h *Handlers) CancelOrder(ctx context.Context, req apigen.CancelOrderRequestObject) (apigen.CancelOrderResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.CancelOrder(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.CancelOrder200JSONResponse(toOrder(v)), nil
}

func (h *Handlers) CancelPlacement(ctx context.Context, req apigen.CancelPlacementRequestObject) (apigen.CancelPlacementResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.CancelPlacement(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.CancelPlacement200JSONResponse(toAdvertiserPlacement(v)), nil
}

func (h *Handlers) AcceptPlacement(ctx context.Context, req apigen.AcceptPlacementRequestObject) (apigen.AcceptPlacementResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.AcceptPlacement(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.AcceptPlacement200JSONResponse(toAdvertiserPlacement(v)), nil
}

func (h *Handlers) CreatePlacementClaim(ctx context.Context, req apigen.CreatePlacementClaimRequestObject) (apigen.CreatePlacementClaimResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.CreateClaim(ctx, org, user, req.Id, app.ClaimInput{
		Reason: domain.ClaimReason(req.Body.Reason), Text: req.Body.Text, IdemKey: deref(req.Params.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return apigen.CreatePlacementClaim201JSONResponse(toAdvertiserPlacement(v)), nil
}

func (h *Handlers) CreateOrderPostVersion(ctx context.Context, req apigen.CreateOrderPostVersionRequestObject) (apigen.CreateOrderPostVersionResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.CreateOrderPostVersion(ctx, org, user, req.Id, deref(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return apigen.CreateOrderPostVersion201JSONResponse(toOrder(v)), nil
}
