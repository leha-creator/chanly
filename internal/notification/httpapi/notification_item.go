package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
)

func (h *Handlers) GetNotification(ctx context.Context, r apigen.GetNotificationRequestObject) (apigen.GetNotificationResponseObject, error) {
	id, err := user(ctx)
	if err != nil {
		return nil, err
	}
	n, err := h.svc.GetNotification(ctx, id, r.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetNotification200JSONResponse(toAPI(n)), nil
}
