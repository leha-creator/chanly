package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
)

func (h *Handlers) GetCart(ctx context.Context, _ apigen.GetCartRequestObject) (apigen.GetCartResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetCart(ctx, org)
	if err != nil {
		return nil, err
	}
	return apigen.GetCart200JSONResponse(toCart(v)), nil
}

func (h *Handlers) AddCartItem(ctx context.Context, req apigen.AddCartItemRequestObject) (apigen.AddCartItemResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.AddCartItem(ctx, org, user, req.Body.ChannelId, req.Body.FormatId)
	if err != nil {
		return nil, err
	}
	return apigen.AddCartItem201JSONResponse(toCart(v)), nil
}

func (h *Handlers) UpdateCartItem(ctx context.Context, req apigen.UpdateCartItemRequestObject) (apigen.UpdateCartItemResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.UpdateCartItem(ctx, org, req.Id, req.Body.FormatId)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateCartItem200JSONResponse(toCart(v)), nil
}

func (h *Handlers) DeleteCartItem(ctx context.Context, req apigen.DeleteCartItemRequestObject) (apigen.DeleteCartItemResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.DeleteCartItem(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.DeleteCartItem200JSONResponse(toCart(v)), nil
}
