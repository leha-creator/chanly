package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

func connectionOwner(ctx context.Context) (uuid.UUID, uuid.UUID, error) {
	org, err := blogger(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	p, _ := httpx.PrincipalFrom(ctx)
	return org, p.UserID, nil
}

func connectionView(v *app.ConnectionView) apigen.ChannelConnection {
	c := v.Connection
	out := apigen.ChannelConnection{Id: c.ID, Status: apigen.ChannelConnectionStatus(c.Status), ExpiresAt: c.ExpiresAt,
		ChannelId: c.ChannelID, ChannelName: opt(c.ChannelName), Candidates: []apigen.ChannelConnectionCandidate{}}
	if c.Reason != "" {
		r := apigen.ChannelConnectionReason(c.Reason)
		out.Reason = &r
	}
	for _, item := range v.Candidates {
		out.Candidates = append(out.Candidates, apigen.ChannelConnectionCandidate{Id: item.ID, Title: item.Title, Link: item.Link, IconUrl: item.IconURL})
	}
	return out
}

func (h *Handlers) CreateChannelConnection(ctx context.Context, req apigen.CreateChannelConnectionRequestObject) (apigen.CreateChannelConnectionResponseObject, error) {
	org, user, err := connectionOwner(ctx)
	if err != nil {
		return nil, err
	}
	var v *app.ConnectionView
	if req.Body != nil {
		v, err = h.svc.CreateConnectionByName(ctx, org, user, req.Body.ChannelName)
	} else {
		v, err = h.svc.CreateConnection(ctx, org, user)
	}
	if err != nil {
		return nil, err
	}
	c := connectionView(v)
	return apigen.CreateChannelConnection201JSONResponse{Id: c.Id, Status: apigen.CreateChannelConnection201JSONResponseBodyStatus(c.Status),
		ExpiresAt: c.ExpiresAt, Candidates: c.Candidates, ChannelName: c.ChannelName, BotUrl: opt(v.BotURL)}, nil
}

func (h *Handlers) GetChannelConnection(ctx context.Context, req apigen.GetChannelConnectionRequestObject) (apigen.GetChannelConnectionResponseObject, error) {
	org, user, err := connectionOwner(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetConnection(ctx, org, user, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetChannelConnection200JSONResponse(connectionView(v)), nil
}

func (h *Handlers) RefreshChannelConnection(ctx context.Context, req apigen.RefreshChannelConnectionRequestObject) (apigen.RefreshChannelConnectionResponseObject, error) {
	org, user, err := connectionOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.RefreshConnection(ctx, org, user, req.Id); err != nil {
		return nil, err
	}
	return apigen.RefreshChannelConnection202Response{}, nil
}

func (h *Handlers) ClaimChannelConnection(ctx context.Context, req apigen.ClaimChannelConnectionRequestObject) (apigen.ClaimChannelConnectionResponseObject, error) {
	org, user, err := connectionOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.svc.ClaimConnection(ctx, org, user, req.Id, req.Body.ChannelId); err != nil {
		return nil, err
	}
	return apigen.ClaimChannelConnection202Response{}, nil
}
