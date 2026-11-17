package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
)

func (h *Handlers) GetTelegramBot(ctx context.Context, _ apigen.GetTelegramBotRequestObject) (apigen.GetTelegramBotResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	connected, err := h.svc.TelegramBotConnected(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.GetTelegramBot200JSONResponse{Connected: connected}, nil
}
func (h *Handlers) CreateTelegramBotConnection(ctx context.Context, _ apigen.CreateTelegramBotConnectionRequestObject) (apigen.CreateTelegramBotConnectionResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, link, err := h.svc.CreateTelegramBotConnection(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.CreateTelegramBotConnection201JSONResponse{Id: c.ID, Status: apigen.TelegramBotConnectionCreatedStatus(c.Status), ExpiresAt: c.ExpiresAt, BotUrl: link}, nil
}
func (h *Handlers) GetTelegramBotConnection(ctx context.Context, r apigen.GetTelegramBotConnectionRequestObject) (apigen.GetTelegramBotConnectionResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.TelegramBotConnection(ctx, p, r.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetTelegramBotConnection200JSONResponse{Id: c.ID, Status: apigen.TelegramBotConnectionStatus(c.Status), ExpiresAt: c.ExpiresAt}, nil
}
func (h *Handlers) DisconnectTelegramBot(ctx context.Context, _ apigen.DisconnectTelegramBotRequestObject) (apigen.DisconnectTelegramBotResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.DisconnectTelegramBot(ctx, p); err != nil {
		return nil, err
	}
	return apigen.DisconnectTelegramBot204Response{}, nil
}
