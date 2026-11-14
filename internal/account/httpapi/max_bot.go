package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
)

func (h *Handlers) GetMaxBot(ctx context.Context, _ apigen.GetMaxBotRequestObject) (apigen.GetMaxBotResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	connected, err := h.svc.MAXBotConnected(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.GetMaxBot200JSONResponse{Connected: connected}, nil
}
func (h *Handlers) CreateMaxBotConnection(ctx context.Context, _ apigen.CreateMaxBotConnectionRequestObject) (apigen.CreateMaxBotConnectionResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, link, err := h.svc.CreateMAXBotConnection(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.CreateMaxBotConnection201JSONResponse{Id: c.ID, Status: apigen.MaxBotConnectionCreatedStatus(c.Status), ExpiresAt: c.ExpiresAt, BotUrl: link}, nil
}
func (h *Handlers) GetMaxBotConnection(ctx context.Context, r apigen.GetMaxBotConnectionRequestObject) (apigen.GetMaxBotConnectionResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.MAXBotConnection(ctx, p, r.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetMaxBotConnection200JSONResponse{Id: c.ID, Status: apigen.MaxBotConnectionStatus(c.Status), ExpiresAt: c.ExpiresAt}, nil
}
func (h *Handlers) DisconnectMaxBot(ctx context.Context, _ apigen.DisconnectMaxBotRequestObject) (apigen.DisconnectMaxBotResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.DisconnectMAXBot(ctx, p); err != nil {
		return nil, err
	}
	return apigen.DisconnectMaxBot204Response{}, nil
}
