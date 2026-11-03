package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/adminpage"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Операции администратора модуля размещений (этап «API администратора»): модерация поста (S-17),
// размещения (R-5, R-6), споры и переписка (S-18, S-19, R-9 … R-11). Роли — матрица R-1.

// AdminPayouts — выплаты размещения для карточки (модуль payout, bootstrap).
type AdminPayouts interface {
	// PlacementPayouts — выплаты размещения новые сверху и признак, что последнюю можно повторить.
	PlacementPayouts(ctx context.Context, p *httpx.Principal, placementID uuid.UUID) ([]apigen.AdminPayout, bool, error)
}

// AdminCreatives — креатив версии поста в ОРД (модуль marking, bootstrap); nil — маркировка выключена.
type AdminCreatives interface {
	VersionCreative(ctx context.Context, versionID uuid.UUID) (*apigen.OrdObject, error)
}

// AdminPorts — порты ответов администратора.
type AdminPorts struct {
	Refs      adminref.Directory
	Payouts   AdminPayouts
	Creatives AdminCreatives
}

// SetAdminPorts подключает порты ответов администратора (bootstrap).
func (h *Handlers) SetAdminPorts(p AdminPorts) { h.admin = p }

func (h *Handlers) adminCtx(ctx context.Context, op string, roles []string) (context.Context, *httpx.Principal, error) {
	return httpx.AdminContext(ctx, h.log, op, roles...)
}

// orgRef — ссылка на организацию: ИНН всегда с маской (D-3).
func orgRef(r adminref.Refs, id uuid.UUID) apigen.AdminOrganizationRef {
	o := r.Org(id)
	return apigen.AdminOrganizationRef{Id: id, Name: o.Name, Inn: logx.MaskINN(o.INN)}
}

func userRef(p *httpx.Principal, r adminref.Refs, id *uuid.UUID) *apigen.AdminUserRef {
	if id == nil {
		return nil
	}
	return &apigen.AdminUserRef{Id: *id, Email: httpx.EmailFor(p, r.Emails[*id])}
}

func (h *Handlers) resolve(ctx context.Context, c *adminref.Collector) (adminref.Refs, error) {
	if h.admin.Refs == nil {
		return adminref.Refs{}, nil
	}
	return c.Resolve(ctx, h.admin.Refs)
}

func dateStart(d *time.Time, days int) *time.Time {
	if d == nil {
		return nil
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, moscow).AddDate(0, 0, days).UTC()
	return &t
}

// moscow — даты фильтров администратора по Москве.
var moscow = time.FixedZone("MSK", 3*60*60)

// ---------------------------------------------------------------- модерация поста

func (h *Handlers) ListAdminOrders(ctx context.Context, req apigen.ListAdminOrdersRequestObject) (apigen.ListAdminOrdersResponseObject, error) {
	ctx, _, err := h.adminCtx(ctx, "Admin.ListOrders", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	q := app.AdminOrderQuery{Query: deref(req.Params.Query), Offset: pg.Offset, Limit: pg.Limit}
	if req.Params.Status != nil {
		q.Status = string(*req.Params.Status)
	}
	rows, total, err := h.svc.AdminOrders(ctx, q)
	if err != nil {
		return nil, err
	}
	var c adminref.Collector
	for _, r := range rows {
		c.Org(r.OrganizationID)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AdminOrderListItem, 0, len(rows))
	for _, r := range rows {
		from := r.PublishFrom.UTC()
		items = append(items, apigen.AdminOrderListItem{
			Id: r.ID, Number: r.Number, Advertiser: orgRef(refs, r.OrganizationID), CampaignName: r.CampaignName,
			Status: apigen.OrderStatus(r.Status), TotalAmount: int64(r.TotalAmount), PlacementsCount: r.PlacementsCount,
			PublishFrom: &from, WaitingSince: utc(r.WaitingSince), Urgent: r.Urgent, CreatedAt: utc(r.CreatedAt),
		})
	}
	return apigen.ListAdminOrders200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func (h *Handlers) adminOrder(ctx context.Context, p *httpx.Principal, v app.AdminOrderView) (apigen.AdminOrder, error) {
	var c adminref.Collector
	c.Org(v.Order.OrganizationID)
	ps := make([]domain.Placement, 0, len(v.Placements))
	for _, pv := range v.Placements {
		c.Org(pv.Placement.BloggerOrgID)
		ps = append(ps, pv.Placement)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return apigen.AdminOrder{}, err
	}
	_, total := counts(v.Counts)
	items := make([]apigen.AdminPlacementListItem, 0, len(v.Placements))
	for _, pv := range v.Placements {
		items = append(items, adminPlacementItem(refs, pv.Placement, pv.Channel, v.Order.Number, v.Order.OrganizationID, nil))
	}
	o := v.Order
	out := apigen.AdminOrder{
		Id: o.ID, Number: o.Number, Advertiser: orgRef(refs, o.OrganizationID), CampaignId: o.CampaignID,
		CampaignName: v.CampaignName, Status: apigen.OrderStatus(o.Status), TotalAmount: int64(o.TotalAmount),
		PlacementsCount: total, PublishFrom: utcPtr(v.PublishFrom), WaitingSince: utc(v.WaitingSince), Urgent: v.Urgent,
		CreatedAt: utc(o.CreatedAt), CurrentPostVersion: toPostVersion(v.Version), Placements: items,
	}
	if len(v.Version.Links) > 0 {
		links := v.Version.Links
		out.Links = &links
	}
	if h.admin.Creatives != nil {
		cr, err := h.admin.Creatives.VersionCreative(ctx, v.Version.ID)
		if err != nil {
			return apigen.AdminOrder{}, err
		}
		out.Creative = cr
	}
	return out, nil
}

func (h *Handlers) GetAdminOrder(ctx context.Context, req apigen.GetAdminOrderRequestObject) (apigen.GetAdminOrderResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.GetOrder", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.AdminOrder(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := h.adminOrder(ctx, p, v)
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminOrder200JSONResponse(out), nil
}

func (h *Handlers) ApproveOrderPost(ctx context.Context, req apigen.ApproveOrderPostRequestObject) (apigen.ApproveOrderPostResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.ApproveOrderPost", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.ApproveOrderPost(ctx, req.Id, p.UserID)
	if err != nil {
		return nil, err
	}
	out, err := h.adminOrder(ctx, p, v)
	if err != nil {
		return nil, err
	}
	return apigen.ApproveOrderPost200JSONResponse(out), nil
}

func (h *Handlers) RejectOrderPost(ctx context.Context, req apigen.RejectOrderPostRequestObject) (apigen.RejectOrderPostResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.RejectOrderPost", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.RejectOrderPost(ctx, req.Id, p.UserID, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	out, err := h.adminOrder(ctx, p, v)
	if err != nil {
		return nil, err
	}
	return apigen.RejectOrderPost200JSONResponse(out), nil
}

// ---------------------------------------------------------------- размещения

func adminPlacementItem(refs adminref.Refs, p domain.Placement, ch domain.ChannelRef, orderNumber int64, advertiser uuid.UUID,
	removalOverdue *bool) apigen.AdminPlacementListItem {
	if ch.ID == uuid.Nil {
		ch.ID = p.ChannelID
	}
	return apigen.AdminPlacementListItem{
		Id: p.ID, OrderId: p.OrderID, OrderNumber: orderNumber, Advertiser: orgRef(refs, advertiser),
		Blogger: orgRef(refs, p.BloggerOrgID), Channel: channelRef(ch), Status: apigen.PlacementStatus(p.Status),
		StatusReason: opt(p.StatusReason), StatusChangedAt: utc(p.StatusChangedAt), TotalAmount: int64(p.TotalAmount),
		RefundAmount: moneyPtr(p.RefundAmount), RemovalOverdue: removalOverdue,
		FormatId: p.FormatID, TopHours: p.TopHours, DurationHours: p.DurationHours,
		WindowStart: utc(p.WindowStart), WindowEnd: utc(p.WindowEnd), TimeFrom: p.TimeFrom.String(), TimeTo: p.TimeTo.String(),
		AcceptDeadlineAt: utcPtr(p.AcceptDeadlineAt), ScheduleDeadlineAt: utcPtr(p.ScheduleDeadlineAt),
		PublishAt: utcPtr(p.PublishAt), RemoveAt: utcPtr(p.RemoveAt), PublishedAt: utcPtr(p.PublishedAt),
		RemovedAt: utcPtr(p.RemovedAt), ClaimDeadlineAt: utcPtr(p.ClaimDeadlineAt), CreatedAt: utc(p.CreatedAt),
	}
}

func (h *Handlers) ListAdminPlacements(ctx context.Context, req apigen.ListAdminPlacementsRequestObject) (apigen.ListAdminPlacementsResponseObject, error) {
	ctx, _, err := h.adminCtx(ctx, "Admin.ListPlacements", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	prm := req.Params
	pg, err := adminpage.Parse(prm.Cursor, prm.Limit)
	if err != nil {
		return nil, err
	}
	f := domain.AdminPlacementFilter{ChannelID: prm.ChannelId, OrderID: prm.OrderId, RemovalOverdue: deref(prm.RemovalOverdue),
		Offset: pg.Offset, Limit: pg.Limit}
	if prm.Status != nil {
		s := domain.Status(*prm.Status)
		f.Status = &s
	}
	if prm.From != nil {
		f.CreatedFrom = dateStart(&prm.From.Time, 0)
	}
	if prm.To != nil {
		f.CreatedTo = dateStart(&prm.To.Time, 1)
	}
	rows, total, err := h.svc.AdminPlacements(ctx, f)
	if err != nil {
		return nil, err
	}
	var c adminref.Collector
	channels := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		c.Org(r.AdvertiserID)
		c.Org(r.Placement.BloggerOrgID)
		channels = append(channels, r.Placement.ChannelID)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	chRefs, err := h.svc.ChannelRefs(ctx, channels)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AdminPlacementListItem, 0, len(rows))
	for _, r := range rows {
		overdue := r.RemovalOverdue
		items = append(items, adminPlacementItem(refs, r.Placement, chRefs[r.Placement.ChannelID], r.OrderNumber, r.AdvertiserID, &overdue))
	}
	return apigen.ListAdminPlacements200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func disputeBrief(p *httpx.Principal, refs adminref.Refs, d domain.Dispute, now time.Time) apigen.AdminDisputeBrief {
	out := apigen.AdminDisputeBrief{
		Id: d.ID, Reason: apigen.AdminDisputeBriefReason(d.Reason), OpenedByType: apigen.AdminDisputeBriefOpenedByType(d.OpenedBy),
		OpenedAt: utc(d.CreatedAt), DeadlineAt: utc(d.DeadlineAt), Overdue: app.DisputeOverdue(d, now),
		SuggestedShare: d.SuggestedShare, PerformanceShare: d.PerformanceShare, ResolvedAt: utcPtr(d.ResolvedAt),
		Comment: opt(d.ResolutionComment), ResolvedBy: userRef(p, refs, d.ResolvedBy),
	}
	if d.Resolved() {
		r := apigen.AdminDisputeBriefResolution(d.Resolution)
		out.Resolution = &r
	}
	return out
}

func (h *Handlers) adminPlacement(ctx context.Context, p *httpx.Principal, v app.AdminPlacementView) (apigen.AdminPlacement, error) {
	var c adminref.Collector
	c.Org(v.AdvertiserID)
	c.Org(v.Placement.BloggerOrgID)
	if v.Dispute != nil {
		c.User(v.Dispute.ResolvedBy)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return apigen.AdminPlacement{}, err
	}
	payouts := []apigen.AdminPayout{}
	retryable := false
	if h.admin.Payouts != nil {
		if payouts, retryable, err = h.admin.Payouts.PlacementPayouts(ctx, p, v.Placement.ID); err != nil {
			return apigen.AdminPlacement{}, err
		}
	}
	pl := v.Placement
	item := adminPlacementItem(refs, pl, v.Channel, v.OrderNumber, v.AdvertiserID, &v.RemovalOverdue)
	actions := domain.AdminActions(&pl, v.Publication, v.Dispute, retryable, httpx.IsSenior(p))
	acts := make([]apigen.AdminPlacementAction, 0, len(actions))
	for _, a := range actions {
		acts = append(acts, apigen.AdminPlacementAction(a))
	}
	violations := make([]apigen.PlacementViolation, 0, len(v.Violations))
	for _, vl := range v.Violations {
		violations = append(violations, apigen.PlacementViolation{Kind: apigen.PlacementViolationKind(vl.Kind),
			OccurredAt: utc(vl.OccurredAt), Source: apigen.PlacementViolationSource(vl.Source)})
	}
	out := apigen.AdminPlacement{
		Id: item.Id, OrderId: item.OrderId, OrderNumber: item.OrderNumber, Advertiser: item.Advertiser, Blogger: item.Blogger,
		Channel: item.Channel, Status: item.Status, StatusReason: item.StatusReason, StatusChangedAt: item.StatusChangedAt,
		TotalAmount: item.TotalAmount, BloggerAmount: int64(pl.BloggerAmount), FeeAmount: int64(pl.FeeAmount),
		PayoutAmount: moneyPtr(pl.PayoutAmount), RefundAmount: item.RefundAmount, PerformanceShare: pl.PerformanceShare,
		RemovalOverdue: item.RemovalOverdue, FormatId: item.FormatId, TopHours: item.TopHours, DurationHours: item.DurationHours,
		WindowStart: item.WindowStart, WindowEnd: item.WindowEnd, TimeFrom: item.TimeFrom, TimeTo: item.TimeTo,
		AcceptDeadlineAt: item.AcceptDeadlineAt, ScheduleDeadlineAt: item.ScheduleDeadlineAt, PublishAt: item.PublishAt,
		RemoveAt: item.RemoveAt, PublishedAt: item.PublishedAt, RemovedAt: item.RemovedAt, ClaimDeadlineAt: item.ClaimDeadlineAt,
		CreatedAt: item.CreatedAt, History: toHistory(v.History), AvailableActions: acts, Violations: violations,
		Payouts: payouts, Claim: claim(v.Claim), PublicationUrl: publicationURL(v.Publication), Views: views(pl, v.Publication),
	}
	if v.Publication != nil && v.Publication.RemovalReason != "" {
		r := apigen.AdminPlacementRemovalReason(v.Publication.RemovalReason)
		out.RemovalReason = &r
	}
	if v.Dispute != nil {
		b := disputeBrief(p, refs, *v.Dispute, h.svc.Clock.Now())
		out.Dispute = &b
	}
	return out, nil
}

func (h *Handlers) placementResponse(ctx context.Context, p *httpx.Principal, id uuid.UUID) (apigen.AdminPlacement, error) {
	v, err := h.svc.AdminPlacement(ctx, id)
	if err != nil {
		return apigen.AdminPlacement{}, err
	}
	return h.adminPlacement(ctx, p, v)
}

func (h *Handlers) GetAdminPlacement(ctx context.Context, req apigen.GetAdminPlacementRequestObject) (apigen.GetAdminPlacementResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.GetPlacement", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	out, err := h.placementResponse(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminPlacement200JSONResponse(out), nil
}

func (h *Handlers) AdminCancelPlacement(ctx context.Context, req apigen.AdminCancelPlacementRequestObject) (apigen.AdminCancelPlacementResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.CancelPlacement", httpx.AdminSeniorOnly)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.CancelByAdmin(ctx, req.Id, p.UserID, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	out, err := h.adminPlacement(ctx, p, v)
	if err != nil {
		return nil, err
	}
	return apigen.AdminCancelPlacement200JSONResponse(out), nil
}

func (h *Handlers) AdminMarkRemoved(ctx context.Context, req apigen.AdminMarkRemovedRequestObject) (apigen.AdminMarkRemovedResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.MarkRemoved", httpx.AdminSeniorOnly)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.MarkRemovedByAdmin(ctx, req.Id, p.UserID, req.Body.RemovedAt, req.Body.Reason)
	if err != nil {
		return nil, err
	}
	out, err := h.adminPlacement(ctx, p, v)
	if err != nil {
		return nil, err
	}
	return apigen.AdminMarkRemoved200JSONResponse(out), nil
}

func (h *Handlers) AdminOpenDispute(ctx context.Context, req apigen.AdminOpenDisputeRequestObject) (apigen.AdminOpenDisputeResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.OpenDispute", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.OpenDisputeByAdmin(ctx, req.Id, p.UserID, req.Body.Reason); err != nil {
		return nil, err
	}
	out, err := h.placementResponse(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.AdminOpenDispute200JSONResponse(out), nil
}

func (h *Handlers) AdminRemovePost(ctx context.Context, req apigen.AdminRemovePostRequestObject) (apigen.AdminRemovePostResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.RemovePost", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.RemovePostByAdmin(ctx, req.Id, p.UserID, req.Body.Reason); err != nil {
		return nil, err
	}
	out, err := h.placementResponse(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.AdminRemovePost200JSONResponse(out), nil
}

// ---------------------------------------------------------------- споры

func (h *Handlers) ListAdminDisputes(ctx context.Context, req apigen.ListAdminDisputesRequestObject) (apigen.ListAdminDisputesResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.ListDisputes", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	pg, err := adminpage.Parse(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	state := domain.DisputesOpen
	if req.Params.State != nil {
		state = domain.DisputeState(*req.Params.State)
	}
	rows, total, err := h.svc.AdminDisputeList(ctx, state, deref(req.Params.Overdue), pg.Offset, pg.Limit)
	if err != nil {
		return nil, err
	}
	var c adminref.Collector
	channels := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		c.Org(r.AdvertiserID)
		c.Org(r.BloggerOrgID)
		c.User(r.Dispute.ResolvedBy)
		channels = append(channels, r.ChannelID)
	}
	refs, err := h.resolve(ctx, &c)
	if err != nil {
		return nil, err
	}
	chRefs, err := h.svc.ChannelRefs(ctx, channels)
	if err != nil {
		return nil, err
	}
	now := h.svc.Clock.Now()
	items := make([]apigen.AdminDisputeListItem, 0, len(rows))
	for _, r := range rows {
		b := disputeBrief(p, refs, r.Dispute, now)
		ch := chRefs[r.ChannelID]
		if ch.ID == uuid.Nil {
			ch.ID = r.ChannelID
		}
		items = append(items, apigen.AdminDisputeListItem{
			Id: b.Id, Reason: apigen.AdminDisputeListItemReason(b.Reason), OpenedByType: apigen.AdminDisputeListItemOpenedByType(b.OpenedByType),
			OpenedAt: b.OpenedAt, DeadlineAt: b.DeadlineAt, Overdue: b.Overdue, SuggestedShare: b.SuggestedShare,
			PerformanceShare: b.PerformanceShare, ResolvedAt: b.ResolvedAt, Comment: b.Comment, ResolvedBy: b.ResolvedBy,
			Resolution: (*apigen.AdminDisputeListItemResolution)(b.Resolution), PlacementId: r.Dispute.PlacementID,
			PlacementStatus: apigen.PlacementStatus(r.PlacementStatus), OrderNumber: r.OrderNumber, TotalAmount: int64(r.TotalAmount),
			Advertiser: orgRef(refs, r.AdvertiserID), Blogger: orgRef(refs, r.BloggerOrgID), Channel: channelRef(ch),
		})
	}
	return apigen.ListAdminDisputes200JSONResponse{Items: items, NextCursor: pg.Next(total), Total: &total}, nil
}

func toMessages(list []domain.DisputeMessage) []apigen.DisputeMessage {
	out := make([]apigen.DisputeMessage, 0, len(list))
	for _, m := range list {
		out = append(out, toMessage(m))
	}
	return out
}

func toMessage(m domain.DisputeMessage) apigen.DisputeMessage {
	out := apigen.DisputeMessage{Id: m.ID, AuthorType: apigen.DisputeMessageAuthorType(m.Author), Text: m.Text,
		CreatedAt: utc(m.CreatedAt)}
	if m.Recipient != "" {
		r := apigen.DisputeMessageRecipient(m.Recipient)
		out.Recipient = &r
	}
	return out
}

func (h *Handlers) adminDispute(ctx context.Context, p *httpx.Principal, id uuid.UUID) (apigen.AdminDispute, error) {
	v, err := h.svc.AdminDisputeByID(ctx, id)
	if err != nil {
		return apigen.AdminDispute{}, err
	}
	card, err := h.adminPlacement(ctx, p, v.Card)
	if err != nil {
		return apigen.AdminDispute{}, err
	}
	b := card.Dispute
	if b == nil {
		bb := disputeBrief(p, adminref.Refs{}, v.Dispute, h.svc.Clock.Now())
		b = &bb
	}
	out := apigen.AdminDispute{
		Id: b.Id, Reason: apigen.AdminDisputeReason(b.Reason), OpenedByType: apigen.AdminDisputeOpenedByType(b.OpenedByType),
		OpenedAt: b.OpenedAt, DeadlineAt: b.DeadlineAt, Overdue: b.Overdue, SuggestedShare: b.SuggestedShare,
		PerformanceShare: b.PerformanceShare, ResolvedAt: b.ResolvedAt, Comment: b.Comment, ResolvedBy: b.ResolvedBy,
		Resolution: (*apigen.AdminDisputeResolution)(b.Resolution), PlacementId: card.Id, PlacementStatus: card.Status,
		OrderNumber: card.OrderNumber, TotalAmount: card.TotalAmount, Advertiser: card.Advertiser, Blogger: card.Blogger,
		Channel: card.Channel, Placement: card, Messages: toMessages(v.Messages),
	}
	if v.Preview != nil && v.Dispute.SuggestedShare != nil {
		out.Preview = &apigen.DisputePreview{Resolution: apigen.DisputeResolution(domain.ResolutionProportional),
			Share: *v.Dispute.SuggestedShare, TotalAmount: card.TotalAmount, BloggerAmount: int64(v.Preview.PayoutAmount),
			FeeAmount: int64(v.Preview.FeeCharged), RefundAmount: int64(v.Preview.RefundAmount)}
	}
	return out, nil
}

func (h *Handlers) GetAdminDispute(ctx context.Context, req apigen.GetAdminDisputeRequestObject) (apigen.GetAdminDisputeResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.GetDispute", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	out, err := h.adminDispute(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.GetAdminDispute200JSONResponse(out), nil
}

func (h *Handlers) PreviewDisputeResolution(ctx context.Context, req apigen.PreviewDisputeResolutionRequestObject) (apigen.PreviewDisputeResolutionResponseObject, error) {
	ctx, _, err := h.adminCtx(ctx, "Admin.PreviewDispute", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.PreviewResolution(ctx, req.Id, domain.Resolution(req.Params.Resolution), req.Params.Share)
	if err != nil {
		return nil, err
	}
	d := r.Distribution
	return apigen.PreviewDisputeResolution200JSONResponse{Resolution: req.Params.Resolution, Share: r.Share,
		TotalAmount: int64(r.Placement.TotalAmount), BloggerAmount: int64(d.PayoutAmount), FeeAmount: int64(d.FeeCharged),
		RefundAmount: int64(d.RefundAmount)}, nil
}

func (h *Handlers) ResolveDispute(ctx context.Context, req apigen.ResolveDisputeRequestObject) (apigen.ResolveDisputeResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.ResolveDispute", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	placementID, err := h.svc.DisputePlacementID(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.ResolveDispute(ctx, app.ResolveInput{PlacementID: placementID,
		Resolution: domain.Resolution(req.Body.Resolution), Share: req.Body.Share, Comment: req.Body.Comment,
		AdminID: p.UserID}); err != nil {
		return nil, err
	}
	out, err := h.adminDispute(ctx, p, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ResolveDispute200JSONResponse(out), nil
}

func (h *Handlers) ListAdminDisputeMessages(ctx context.Context, req apigen.ListAdminDisputeMessagesRequestObject) (apigen.ListAdminDisputeMessagesResponseObject, error) {
	ctx, _, err := h.adminCtx(ctx, "Admin.ListDisputeMessages", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.AdminMessages(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ListAdminDisputeMessages200JSONResponse{Items: toMessages(list)}, nil
}

func (h *Handlers) CreateAdminDisputeMessage(ctx context.Context, req apigen.CreateAdminDisputeMessageRequestObject) (apigen.CreateAdminDisputeMessageResponseObject, error) {
	ctx, p, err := h.adminCtx(ctx, "Admin.CreateDisputeMessage", httpx.AdminAny)
	if err != nil {
		return nil, err
	}
	m, err := h.svc.PostAdminMessage(ctx, req.Id, p.UserID, domain.MessageRecipient(req.Body.To), req.Body.Text)
	if err != nil {
		return nil, err
	}
	return apigen.CreateAdminDisputeMessage201JSONResponse(toMessage(m)), nil
}

// ---------------------------------------------------------------- переписка сторон

func (h *Handlers) ListPlacementDisputeMessages(ctx context.Context, req apigen.ListPlacementDisputeMessagesRequestObject) (apigen.ListPlacementDisputeMessagesResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.PartyMessages(ctx, domain.AuthorAdvertiser, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ListPlacementDisputeMessages200JSONResponse{Items: toMessages(list)}, nil
}

func (h *Handlers) CreatePlacementDisputeMessage(ctx context.Context, req apigen.CreatePlacementDisputeMessageRequestObject) (apigen.CreatePlacementDisputeMessageResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	m, err := h.svc.PostPartyMessage(ctx, domain.AuthorAdvertiser, org, user, req.Id, req.Body.Text)
	if err != nil {
		return nil, err
	}
	return apigen.CreatePlacementDisputeMessage201JSONResponse(toMessage(m)), nil
}

func (h *Handlers) ListBloggerDisputeMessages(ctx context.Context, req apigen.ListBloggerDisputeMessagesRequestObject) (apigen.ListBloggerDisputeMessagesResponseObject, error) {
	org, _, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.PartyMessages(ctx, domain.AuthorBlogger, org, req.Id)
	if err != nil {
		return nil, err
	}
	return apigen.ListBloggerDisputeMessages200JSONResponse{Items: toMessages(list)}, nil
}

func (h *Handlers) CreateBloggerDisputeMessage(ctx context.Context, req apigen.CreateBloggerDisputeMessageRequestObject) (apigen.CreateBloggerDisputeMessageResponseObject, error) {
	org, user, err := blogger(ctx)
	if err != nil {
		return nil, err
	}
	m, err := h.svc.PostPartyMessage(ctx, domain.AuthorBlogger, org, user, req.Id, req.Body.Text)
	if err != nil {
		return nil, err
	}
	return apigen.CreateBloggerDisputeMessage201JSONResponse(toMessage(m)), nil
}
