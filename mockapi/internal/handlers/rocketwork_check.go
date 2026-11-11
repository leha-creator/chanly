package handlers

import (
	"context"
	"net/http"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// The mock never claims a real payment or exposes the trusted runner.
func (h *Handlers) GetAdminRocketworkCheck(ctx context.Context, _ api.GetAdminRocketworkCheckRequestObject) (api.GetAdminRocketworkCheckResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	return api.GetAdminRocketworkCheck200JSONResponse{State: "not_configured"}, nil
}
func (h *Handlers) StartAdminRocketworkCheck(ctx context.Context, _ api.StartAdminRocketworkCheckRequestObject) (api.StartAdminRocketworkCheckResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	return nil, &store.Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "real diagnostic is not configured"}
}
