package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func (h *Handlers) admin(ctx context.Context, op string) (uuid.UUID, error) {
	p, err := httpx.RequireAdmin(ctx, h.log, op, httpx.AdminAny...)
	if err != nil {
		return uuid.Nil, err
	}
	return p.UserID, nil
}

func (h *Handlers) ListAdminChannels(ctx context.Context, req apigen.ListAdminChannelsRequestObject) (apigen.ListAdminChannelsResponseObject, error) {
	if _, err := h.admin(ctx, "Admin.ListChannels"); err != nil {
		return nil, err
	}
	p := req.Params
	r := app.AdminListRequest{RknRecheckDue: p.RknRecheckDue}
	if p.Status != nil {
		s := domain.Status(*p.Status)
		r.Status = &s
	}
	if p.RknStatus != nil {
		s := domain.RknStatus(*p.RknStatus)
		r.RknStatus = &s
	}
	if p.Limit != nil {
		r.Limit = *p.Limit
	}
	if p.Cursor != nil {
		r.Cursor = *p.Cursor
	}
	page, err := h.svc.AdminList(ctx, r)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AdminChannelListItem, 0, len(page.Items))
	for _, it := range page.Items {
		c := it.Channel
		items = append(items, apigen.AdminChannelListItem{
			Id: c.ID, Title: c.Title, Link: opt(c.Link), Organization: orgRef(it.Organization),
			Status: apigen.ChannelStatus(c.Status), StatusReason: opt(c.StatusReason), CategoryId: c.CategoryID,
			SubscribersCount: c.SubscribersCount, RknNumber: opt(c.RknNumber), RknStatus: apigen.RknStatus(c.RknStatus),
			RknRecheckDue: h.svc.RknRecheckDue(c), UpdatedAt: c.UpdatedAt,
		})
	}
	return apigen.ListAdminChannels200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) GetAdminChannel(ctx context.Context, req apigen.GetAdminChannelRequestObject) (apigen.GetAdminChannelResponseObject, error) {
	if _, err := h.admin(ctx, "Admin.GetChannel"); err != nil {
		return nil, err
	}
	v, err := h.svc.AdminGet(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminChannel200JSONResponse(h.adminChannel(v)), nil
}

func (h *Handlers) ApproveChannel(ctx context.Context, req apigen.ApproveChannelRequestObject) (apigen.ApproveChannelResponseObject, error) {
	adminID, err := h.admin(ctx, "Admin.ApproveChannel")
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Approve(ctx, adminID, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ApproveChannel200JSONResponse(h.adminChannel(v)), nil
}

func (h *Handlers) RejectChannel(ctx context.Context, req apigen.RejectChannelRequestObject) (apigen.RejectChannelResponseObject, error) {
	adminID, err := h.admin(ctx, "Admin.RejectChannel")
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Reject(ctx, adminID, req.Id, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	return apigen.RejectChannel200JSONResponse(h.adminChannel(v)), nil
}

func (h *Handlers) SetChannelRknCheck(ctx context.Context, req apigen.SetChannelRknCheckRequestObject) (apigen.SetChannelRknCheckResponseObject, error) {
	adminID, err := h.admin(ctx, "Admin.SetChannelRknCheck")
	if err != nil {
		return nil, err
	}
	comment := ""
	if req.Body.Comment != nil {
		comment = *req.Body.Comment
	}
	v, err := h.svc.SetRknCheck(ctx, adminID, req.Id, domain.RknCheckResult(req.Body.Result), comment)
	if err != nil {
		return nil, err
	}
	return apigen.SetChannelRknCheck200JSONResponse(h.adminChannel(v)), nil
}

// adminChannel — канал для экрана модерации. ИНН владельца маскируется: полное
// значение экрану не нужно (07 §5.4).
func (h *Handlers) adminChannel(v *app.AdminChannelView) apigen.AdminChannel {
	c := v.Channel
	b := bloggerChannel(c, h.svc.FeeRateBP())
	out := apigen.AdminChannel{
		Id: b.Id, Title: b.Title, IconUrl: b.IconUrl, Link: b.Link, IsPublic: b.IsPublic, Status: b.Status,
		StatusReason: b.StatusReason, Description: b.Description, CategoryId: b.CategoryId,
		SubscribersCount: b.SubscribersCount, BotIsAdmin: b.BotIsAdmin, BotPermissions: b.BotPermissions,
		MissingPermissions: b.MissingPermissions, RknNumber: b.RknNumber, RknStatus: b.RknStatus, Rating: b.Rating,
		Prices: b.Prices, Unavailability: unavailability(v.Unavailability),
		Organization: orgRef(v.Organization), MaxDescription: opt(c.MaxDescription),
		MetricsHistory: metricsHistory(v.History), RknCheckedAt: c.RknCheckedAt, RknRecheckDue: v.RknRecheckDue,
		ModeratedAt: c.ModeratedAt, ModerationHistory: moderationHistory(v.Moderation),
	}
	if n := len(v.History); n > 0 {
		last := v.History[n-1]
		out.AvgViews, out.EngagementBp, out.PostsPerWeek = last.AvgViews, last.EngagementBP, last.PostsPerWeek
		out.AnomalyFlags = flags[apigen.AdminChannelAnomalyFlags](last.AnomalyFlags)
	}
	return out
}

func orgRef(o *domain.OrganizationRef) *apigen.AdminOrganizationRef {
	if o == nil {
		return nil
	}
	return &apigen.AdminOrganizationRef{Id: o.ID, Name: o.Name, Inn: logx.MaskINN(o.INN)}
}

func moderationHistory(list []domain.ModerationEntry) []apigen.ChannelModerationEntry {
	out := make([]apigen.ChannelModerationEntry, 0, len(list))
	for _, e := range list {
		item := apigen.ChannelModerationEntry{
			Action: apigen.ChannelModerationEntryAction(e.Action), ActorType: apigen.ChannelModerationEntryActorType(e.ActorType),
			ActorUserId: e.ActorUserID, Comment: opt(e.Comment), CreatedAt: e.At,
		}
		if e.Result != "" {
			r := apigen.ChannelModerationEntryResult(e.Result)
			item.Result = &r
		}
		out = append(out, item)
	}
	return out
}

// senior — операции только для senior (матрица R-1 этапа «API администратора»).
func (h *Handlers) senior(ctx context.Context, op string) (uuid.UUID, error) {
	p, err := httpx.RequireAdmin(ctx, h.log, op, httpx.AdminSeniorOnly...)
	if err != nil {
		return uuid.Nil, err
	}
	return p.UserID, nil
}

func (h *Handlers) BlockChannel(ctx context.Context, req apigen.BlockChannelRequestObject) (apigen.BlockChannelResponseObject, error) {
	adminID, err := h.senior(ctx, "Admin.BlockChannel")
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Block(ctx, adminID, req.Id, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	return apigen.BlockChannel200JSONResponse(h.adminChannel(v)), nil
}

func (h *Handlers) UnblockChannel(ctx context.Context, req apigen.UnblockChannelRequestObject) (apigen.UnblockChannelResponseObject, error) {
	adminID, err := h.senior(ctx, "Admin.UnblockChannel")
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Unblock(ctx, adminID, req.Id, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	return apigen.UnblockChannel200JSONResponse(h.adminChannel(v)), nil
}

func adminCategory(c domain.Category) apigen.AdminChannelCategory {
	return apigen.AdminChannelCategory{Id: c.ID, Slug: c.Slug, Name: c.Name, Position: c.Position, Archived: c.Archived}
}

func (h *Handlers) ListAdminChannelCategories(ctx context.Context, _ apigen.ListAdminChannelCategoriesRequestObject) (apigen.ListAdminChannelCategoriesResponseObject, error) {
	if _, err := h.senior(ctx, "Admin.ListChannelCategories"); err != nil {
		return nil, err
	}
	list, err := h.svc.ListAdminCategories(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AdminChannelCategory, 0, len(list))
	for _, c := range list {
		items = append(items, adminCategory(c))
	}
	return apigen.ListAdminChannelCategories200JSONResponse{Items: items}, nil
}

func (h *Handlers) CreateChannelCategory(ctx context.Context, req apigen.CreateChannelCategoryRequestObject) (apigen.CreateChannelCategoryResponseObject, error) {
	adminID, err := h.senior(ctx, "Admin.CreateChannelCategory")
	if err != nil {
		return nil, err
	}
	c, err := h.svc.CreateCategory(ctx, adminID, domain.CategoryInput{Slug: req.Body.Slug, Name: req.Body.Name,
		Position: req.Body.Position})
	if err != nil {
		return nil, err
	}
	return apigen.CreateChannelCategory201JSONResponse(adminCategory(c)), nil
}

func (h *Handlers) UpdateChannelCategory(ctx context.Context, req apigen.UpdateChannelCategoryRequestObject) (apigen.UpdateChannelCategoryResponseObject, error) {
	adminID, err := h.senior(ctx, "Admin.UpdateChannelCategory")
	if err != nil {
		return nil, err
	}
	in := domain.CategoryInput{Position: req.Body.Position, Archived: req.Body.Archived}
	if req.Body.Name != nil {
		in.Name = *req.Body.Name
	}
	c, err := h.svc.UpdateCategory(ctx, adminID, req.Id, in)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateChannelCategory200JSONResponse(adminCategory(c)), nil
}
