package httpapi

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// cabinetBlogger — кабинет, которому доступны операции каналов (07 §4).
const cabinetBlogger = "blogger"

// Handlers — операции OpenAPI модуля каналов.
type Handlers struct {
	svc *app.Service
	log *slog.Logger
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "channel")}
}

// blogger — организация пользователя с открытым кабинетом блогера.
func blogger(ctx context.Context) (uuid.UUID, error) {
	p, err := httpx.RequireCabinet(ctx, cabinetBlogger)
	if err != nil {
		return uuid.Nil, err
	}
	if p.OrganizationID == nil {
		return uuid.Nil, apperr.Forbidden(httpx.ReasonCabinetRequired).WithDetail("cabinet", cabinetBlogger)
	}
	return *p.OrganizationID, nil
}

func (h *Handlers) ListBloggerChannels(ctx context.Context, _ apigen.ListBloggerChannelsRequestObject) (apigen.ListBloggerChannelsResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, org)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.BloggerChannel, 0, len(list))
	for _, c := range list {
		v, err := h.view(ctx, c)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	h.log.DebugContext(ctx, "blogger channels listed", logx.KeyOp, "Channel.List", "count", len(items))
	return apigen.ListBloggerChannels200JSONResponse{Items: items}, nil
}

func (h *Handlers) CreateChannelLinkCode(ctx context.Context, _ apigen.CreateChannelLinkCodeRequestObject) (apigen.CreateChannelLinkCodeResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	issued, err := h.svc.CreateLinkCode(ctx, org)
	if err != nil {
		return nil, err
	}
	return apigen.CreateChannelLinkCode201JSONResponse{
		Code: issued.Code, ExpiresAt: issued.ExpiresAt, BotName: issued.BotName, Instruction: issued.Instruction,
	}, nil
}

func (h *Handlers) GetBloggerChannel(ctx context.Context, req apigen.GetBloggerChannelRequestObject) (apigen.GetBloggerChannelResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.svc.Get(ctx, org, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := h.view(ctx, c)
	if err != nil {
		return nil, err
	}
	return apigen.GetBloggerChannel200JSONResponse(v), nil
}

func (h *Handlers) UpdateBloggerChannel(ctx context.Context, req apigen.UpdateBloggerChannelRequestObject) (apigen.UpdateBloggerChannelResponseObject, error) {
	org, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	c, err := h.svc.Update(ctx, org, req.Id, domain.ProfileUpdate{
		Description: b.Description, CategoryID: b.CategoryId, RknNumber: b.RknNumber,
	})
	if err != nil {
		return nil, err
	}
	v, err := h.view(ctx, c)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateBloggerChannel200JSONResponse(v), nil
}

func (h *Handlers) ListChannelCategories(ctx context.Context, _ apigen.ListChannelCategoriesRequestObject) (apigen.ListChannelCategoriesResponseObject, error) {
	list, err := h.svc.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ChannelCategory, 0, len(list))
	for _, c := range list {
		items = append(items, apigen.ChannelCategory{Id: c.ID, Slug: c.Slug, Name: c.Name})
	}
	return apigen.ListChannelCategories200JSONResponse{Items: items}, nil
}

// bloggerChannel — канал для кабинета без интервалов недоступности; их добавляет view.
func bloggerChannel(c *domain.Channel, feeRateBP int) apigen.BloggerChannel {
	perms := c.BotPermissions
	if perms == nil {
		perms = []string{}
	}
	return apigen.BloggerChannel{
		Id: c.ID, Title: c.Title, IconUrl: opt(c.IconURL), Link: opt(c.Link), IsPublic: c.IsPublic,
		Status: apigen.ChannelStatus(c.Status), StatusReason: opt(c.StatusReason), Description: opt(c.Description),
		CategoryId: c.CategoryID, SubscribersCount: c.SubscribersCount, BotIsAdmin: c.BotIsAdmin,
		BotPermissions: perms, MissingPermissions: c.MissingPermissions(), RknNumber: opt(c.RknNumber),
		RknStatus: apigen.RknStatus(c.RknStatus), Rating: c.Rating,
		Prices: bloggerPrices(c.Prices, feeRateBP), Unavailability: []apigen.Unavailability{},
	}
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
