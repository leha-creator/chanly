package httpapi

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
)

func (h *Handlers) ListAccountSessions(ctx context.Context, _ apigen.ListAccountSessionsRequestObject) (apigen.ListAccountSessionsResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.svc.ListOwnSessions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := apigen.AccountSessions{Items: []apigen.AccountSession{}}
	for _, s := range rows {
		out.Items = append(out.Items, apigen.AccountSession{Id: s.ID, Current: s.ID == p.SessionID, CreatedAt: s.CreatedAt, LastActiveAt: s.UpdatedAt, ExpiresAt: s.ExpiresAt, UserAgent: s.UserAgent})
	}
	return apigen.ListAccountSessions200JSONResponse(out), nil
}
func (h *Handlers) RevokeAccountSession(ctx context.Context, r apigen.RevokeAccountSessionRequestObject) (apigen.RevokeAccountSessionResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.RevokeOwnSessions(ctx, p, &r.Id); err != nil {
		return nil, err
	}
	return apigen.RevokeAccountSession204Response{}, nil
}
func (h *Handlers) RevokeOtherAccountSessions(ctx context.Context, _ apigen.RevokeOtherAccountSessionsRequestObject) (apigen.RevokeOtherAccountSessionsResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.RevokeOwnSessions(ctx, p, nil); err != nil {
		return nil, err
	}
	return apigen.RevokeOtherAccountSessions204Response{}, nil
}
func emailChangeAPI(c *domain.EmailChange) apigen.EmailChange {
	return apigen.EmailChange{Id: c.ID, ExpiresAt: c.ExpiresAt, ResendAt: c.SentAt.Add(time.Minute)}
}
func (h *Handlers) StartEmailChange(ctx context.Context, r apigen.StartEmailChangeRequestObject) (apigen.StartEmailChangeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.StartEmailChange(ctx, p, string(r.Body.NewEmail))
	if err != nil {
		return nil, err
	}
	return apigen.StartEmailChange202JSONResponse(emailChangeAPI(c)), nil
}
func (h *Handlers) ResendEmailChange(ctx context.Context, r apigen.ResendEmailChangeRequestObject) (apigen.ResendEmailChangeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.ResendEmailChange(ctx, p, r.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ResendEmailChange202JSONResponse(emailChangeAPI(c)), nil
}
func (h *Handlers) ConfirmEmailChange(ctx context.Context, r apigen.ConfirmEmailChangeRequestObject) (apigen.ConfirmEmailChangeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.ConfirmEmailChange(ctx, p, r.Id, r.Body.CurrentCode, r.Body.NewCode); err != nil {
		return nil, err
	}
	return apigen.ConfirmEmailChange204Response{}, nil
}
