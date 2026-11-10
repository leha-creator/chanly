package handlers

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// BotName — имя бота платформы в инструкциях (выдуманное).
const BotName = "@test_ads_bot"

func bloggerChannel(tx *store.Tx, c *store.Channel) api.BloggerChannel {
	out := api.BloggerChannel{
		Id: c.ID, Title: c.Title, IconUrl: strPtr(c.IconURL), Link: strPtr(c.Link), IsPublic: c.IsPublic,
		Status: api.ChannelStatus(c.Status), StatusReason: strPtr(c.StatusReason), Description: strPtr(c.Description),
		CategoryId: c.CategoryID, SubscribersCount: c.Subscribers, BotIsAdmin: c.BotIsAdmin,
		BotPermissions: slices.Clone(c.BotPermissions), RknNumber: strPtr(c.RknNumber),
		RknStatus: api.RknStatus(c.RknStatus), Rating: c.Rating, Prices: []api.BloggerPrice{}, Unavailability: unavailability(c),
	}
	if out.BotPermissions == nil {
		out.BotPermissions = []string{}
	}
	out.MissingPermissions = []string{}
	for _, p := range []string{"write", "delete"} {
		if !slices.Contains(out.BotPermissions, p) {
			out.MissingPermissions = append(out.MissingPermissions, p)
		}
	}
	for _, f := range tx.D.Formats {
		if p := c.Prices[f.ID]; p != nil {
			_, total := store.TotalFor(p.Amount)
			out.Prices = append(out.Prices, api.BloggerPrice{FormatId: f.ID, Amount: p.Amount, IsEnabled: p.IsEnabled, TotalAmount: total})
		}
	}
	return out
}

func (h *Handlers) ListBloggerChannels(ctx context.Context, req api.ListBloggerChannelsRequestObject) (api.ListBloggerChannelsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	items := []api.BloggerChannel{}
	for _, c := range tx.D.Channels {
		if c.OrgID != nil && *c.OrgID == o.ID {
			items = append(items, bloggerChannel(tx, c))
		}
	}
	return api.ListBloggerChannels200JSONResponse{Items: items}, nil
}

func (h *Handlers) CreateChannelLinkCode(ctx context.Context, req api.CreateChannelLinkCodeRequestObject) (api.CreateChannelLinkCodeResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	code := linkCode()
	lc := &store.LinkCode{Code: code, OrgID: o.ID, ExpiresAt: tx.Now.Add(time.Hour)}
	tx.D.LinkCodes = append(tx.D.LinkCodes, lc)
	h.logger(ctx).Info("channel link code issued", "op", "Channel.LinkCode", "organization_id", o.ID) // код в журнал не пишется
	return api.CreateChannelLinkCode201JSONResponse{
		Code: code, ExpiresAt: lc.ExpiresAt, BotName: BotName,
		Instruction: fmt.Sprintf("Добавьте %s администратором канала с правами на публикацию и удаление сообщений, затем отправьте код %s сообщением в канал. "+
			"В тестовом сервере вместо этого вызовите POST /api/v1/_mock/channels/link с этим кодом.", BotName, code),
	}, nil
}

func (h *Handlers) GetBloggerChannel(ctx context.Context, req api.GetBloggerChannelRequestObject) (api.GetBloggerChannelResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.OrgChannel(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetBloggerChannel200JSONResponse(bloggerChannel(tx, c)), nil
}

func (h *Handlers) UpdateBloggerChannel(ctx context.Context, req api.UpdateBloggerChannelRequestObject) (api.UpdateBloggerChannelResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.OrgChannel(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b.CategoryId != nil {
		if tx.Category(*b.CategoryId) == nil {
			return nil, store.Validation("category_id", "unknown category")
		}
		c.CategoryID = b.CategoryId
	}
	if b.Description != nil {
		c.Description = *b.Description
	}
	if b.RknNumber != nil && *b.RknNumber != c.RknNumber {
		// Проверку в реестре РКН заглушка проходит сразу (S-16 делает её вручную).
		// Тот же номер в запросе — не изменение: отметка о проверке остаётся прежней.
		c.RknNumber = *b.RknNumber
		c.RknStatus, c.RknCheckedAt = "not_required", nil
		if c.RknNumber != "" {
			now := tx.Now
			c.RknStatus, c.RknCheckedAt = "confirmed", &now
		}
		h.logger(ctx).Info("[FIX] channel rkn number changed", "op", "Channel.Update", "channel_id", c.ID, "rkn_status", c.RknStatus)
	}
	if c.Status == "rejected" {
		c.Status = "moderation"
	}
	tx.RecheckChannel(c)
	h.logger(ctx).Info("channel updated", "op", "Channel.Update", "channel_id", c.ID, "status", c.Status)
	return api.UpdateBloggerChannel200JSONResponse(bloggerChannel(tx, c)), nil
}

func (h *Handlers) SetBloggerChannelPrices(ctx context.Context, req api.SetBloggerChannelPricesRequestObject) (api.SetBloggerChannelPricesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.OrgChannel(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	prices := map[string]*store.Price{}
	for _, p := range req.Body.Prices {
		if tx.Format(p.FormatId) == nil {
			return nil, store.Validation("prices.format_id", "unknown format: "+p.FormatId)
		}
		if _, dup := prices[p.FormatId]; dup {
			return nil, store.Validation("prices.format_id", "duplicate format: "+p.FormatId)
		}
		prices[p.FormatId] = &store.Price{Amount: p.Amount, IsEnabled: p.IsEnabled}
	}
	c.Prices = prices
	tx.RecheckChannel(c)
	h.logger(ctx).Info("channel prices set", "op", "Channel.Prices", "channel_id", c.ID, "formats", len(prices))
	return api.SetBloggerChannelPrices200JSONResponse(bloggerChannel(tx, c)), nil
}

func (h *Handlers) AddChannelUnavailability(ctx context.Context, req api.AddChannelUnavailabilityRequestObject) (api.AddChannelUnavailabilityResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.OrgChannel(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	u := store.Unavailability{ID: store.NewID(), StartsAt: req.Body.StartsAt.UTC(), EndsAt: req.Body.EndsAt.UTC()}
	if err := checkUnavailability(tx, c, u); err != nil {
		h.logger(ctx).Warn("[FIX] channel unavailability rejected", "op", "Channel.Unavailability", "channel_id", c.ID,
			"code", err.Code, "reason", err.Details["reason"])
		return nil, err
	}
	c.Unavailability = append(c.Unavailability, u)
	h.logger(ctx).Info("channel unavailability added", "op", "Channel.Unavailability", "channel_id", c.ID)
	return api.AddChannelUnavailability201JSONResponse(bloggerChannel(tx, c)), nil
}

// Пределы интервалов недоступности (S-13, 07 §8) — значения по умолчанию бэкенда.
const (
	unavailabilityMaxLength = 90 * 24 * time.Hour
	unavailabilityHorizon   = 365 * 24 * time.Hour
	unavailabilityMaxCount  = 50
)

// checkUnavailability — правила нового интервала: 409 для канала без приёма, иначе 422 с details.reason.
func checkUnavailability(tx *store.Tx, c *store.Channel, u store.Unavailability) *store.Error {
	switch c.Status {
	case "unclaimed", "disconnected", "blocked":
		return store.Conflict("conflict", "channel does not accept unavailability intervals", map[string]any{"status": c.Status})
	}
	if !u.StartsAt.Before(u.EndsAt) || !u.EndsAt.After(tx.Now) {
		return store.Invalid("ends_at", "in_past", "ends_at must be after starts_at and in the future")
	}
	if u.EndsAt.Sub(u.StartsAt) > unavailabilityMaxLength {
		return store.Invalid("ends_at", "too_long", "interval is longer than 90 days")
	}
	if u.StartsAt.After(tx.Now.Add(unavailabilityHorizon)) {
		return store.Invalid("starts_at", "too_far", "interval starts more than 365 days ahead")
	}
	live := 0
	for _, e := range c.Unavailability {
		if !e.EndsAt.After(tx.Now) {
			continue
		}
		live++
		// Соприкосновение границ — не пересечение.
		if u.StartsAt.Before(e.EndsAt) && e.StartsAt.Before(u.EndsAt) {
			return store.Invalid("starts_at", "overlap", "interval overlaps another interval of the channel")
		}
	}
	if live >= unavailabilityMaxCount {
		return store.Invalid("starts_at", "too_many", "channel already has 50 intervals")
	}
	return nil
}

func (h *Handlers) DeleteChannelUnavailability(ctx context.Context, req api.DeleteChannelUnavailabilityRequestObject) (api.DeleteChannelUnavailabilityResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := tx.OrgChannel(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	n := len(c.Unavailability)
	c.Unavailability = slices.DeleteFunc(c.Unavailability, func(u store.Unavailability) bool { return u.ID == req.UnavailabilityId })
	if len(c.Unavailability) == n {
		return nil, store.NotFound("channel_unavailability", req.UnavailabilityId)
	}
	h.logger(ctx).Info("channel unavailability removed", "op", "Channel.Unavailability", "channel_id", c.ID)
	return api.DeleteChannelUnavailability200JSONResponse(bloggerChannel(tx, c)), nil
}

// ---------------------------------------------------------------- размещения блогера

func bloggerPlacement(tx *store.Tx, p *store.Placement) api.BloggerPlacement {
	t := timing(p)
	out := api.BloggerPlacement{
		Id: p.ID, Status: api.PlacementStatus(p.Status), StatusReason: strPtr(p.StatusReason),
		StatusChangedAt: p.StatusChangedAt, BloggerAmount: p.BloggerAmount, PayoutAmount: p.PayoutAmount,
		PublicationUrl: strPtr(p.PublicationURL), Views: p.Views, AvailableActions: []api.BloggerPlacementAction{},
		FormatId: t.FormatId, TopHours: t.TopHours, DurationHours: t.DurationHours, WindowStart: t.WindowStart,
		WindowEnd: t.WindowEnd, TimeFrom: t.TimeFrom, TimeTo: t.TimeTo, AcceptDeadlineAt: t.AcceptDeadlineAt,
		ScheduleDeadlineAt: t.ScheduleDeadlineAt, PublishAt: t.PublishAt, RemoveAt: t.RemoveAt,
		PublishedAt: t.PublishedAt, RemovedAt: t.RemovedAt, ClaimDeadlineAt: t.ClaimDeadlineAt,
	}
	if c, err := tx.Channel(p.ChannelID); err == nil {
		out.Channel = channelRef(c)
	}
	out.Dispute = disputeSummary(p)
	if o, err := tx.Order(p.OrderID); err == nil {
		out.OrderNumber = o.Number
		if adv, err := tx.Org(o.OrgID); err == nil {
			out.AdvertiserName = &adv.Name
		}
		if v := o.Version(p.PostVersionID); v != nil && v.Status == "ready" {
			post := &api.BloggerPost{FinalText: v.FinalText, Erid: v.Erid,
				MediaUrls: versionMediaURLs(tx, o.OrgID, v.MediaIDs, v.TokenCarrier)}
			for _, c := range tx.D.Campaigns {
				if c.ID == o.CampaignID {
					post.ExtraRequirements = c.ExtraRequirements
				}
			}
			out.Post = post
		}
	}
	for code, action := range map[string]api.BloggerPlacementAction{"P-03": "accept", "P-04": "decline", "P-17": "schedule", "P-07": "reschedule"} {
		if store.Allowed(code, p.Status) {
			out.AvailableActions = append(out.AvailableActions, action)
		}
	}
	slices.Sort(out.AvailableActions)
	return out
}

// blgPlacement находит размещение блогера; размещения в created блогер не видит (до P-02).
func blgPlacement(tx *store.Tx, orgID, id uuid.UUID) (*store.Placement, error) {
	p, err := tx.Placement(id)
	if err != nil || p.BloggerOrgID != orgID || p.Status == "created" {
		return nil, store.NotFound("placement", id)
	}
	return p, nil
}

func (h *Handlers) ListBloggerPlacements(ctx context.Context, req api.ListBloggerPlacementsRequestObject) (api.ListBloggerPlacementsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.Placement
	for _, p := range tx.D.Placements {
		if p.BloggerOrgID != o.ID || p.Status == "created" {
			continue
		}
		if (req.Params.Status != nil && string(*req.Params.Status) != p.Status) ||
			(req.Params.ChannelId != nil && *req.Params.ChannelId != p.ChannelID) {
			continue
		}
		mine = append(mine, p)
	}
	mine = newestFirst(mine, func(p *store.Placement) time.Time { return p.StatusChangedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.BloggerPlacement{}
	for _, p := range pg {
		items = append(items, bloggerPlacement(tx, p))
	}
	return api.ListBloggerPlacements200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetBloggerPlacement(ctx context.Context, req api.GetBloggerPlacementRequestObject) (api.GetBloggerPlacementResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	p, err := blgPlacement(tx, o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetBloggerPlacement200JSONResponse(bloggerPlacement(tx, p)), nil
}

func (h *Handlers) bloggerAction(ctx context.Context, id uuid.UUID, act func(*store.Tx, *store.Organization, *store.Placement) error) (api.BloggerPlacement, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return api.BloggerPlacement{}, err
	}
	p, err := blgPlacement(tx, o.ID, id)
	if err != nil {
		return api.BloggerPlacement{}, err
	}
	if err := act(tx, o, p); err != nil {
		return api.BloggerPlacement{}, err
	}
	return bloggerPlacement(tx, p), nil
}

func (h *Handlers) BloggerAcceptPlacement(ctx context.Context, req api.BloggerAcceptPlacementRequestObject) (api.BloggerAcceptPlacementResponseObject, error) {
	out, err := h.bloggerAction(ctx, req.Id, func(tx *store.Tx, o *store.Organization, p *store.Placement) error { return tx.BloggerAccept(o, p) })
	if err != nil {
		return nil, err
	}
	return api.BloggerAcceptPlacement200JSONResponse(out), nil
}

func (h *Handlers) BloggerDeclinePlacement(ctx context.Context, req api.BloggerDeclinePlacementRequestObject) (api.BloggerDeclinePlacementResponseObject, error) {
	out, err := h.bloggerAction(ctx, req.Id, func(tx *store.Tx, o *store.Organization, p *store.Placement) error { return tx.BloggerDecline(p) })
	if err != nil {
		return nil, err
	}
	return api.BloggerDeclinePlacement200JSONResponse(out), nil
}

func (h *Handlers) BloggerSchedulePlacement(ctx context.Context, req api.BloggerSchedulePlacementRequestObject) (api.BloggerSchedulePlacementResponseObject, error) {
	out, err := h.bloggerAction(ctx, req.Id, func(tx *store.Tx, o *store.Organization, p *store.Placement) error {
		return tx.BloggerSchedule(p, string(req.Body.Mode), req.Body.PublishAt)
	})
	if err != nil {
		return nil, err
	}
	return api.BloggerSchedulePlacement200JSONResponse(out), nil
}

func (h *Handlers) BloggerReschedulePlacement(ctx context.Context, req api.BloggerReschedulePlacementRequestObject) (api.BloggerReschedulePlacementResponseObject, error) {
	out, err := h.bloggerAction(ctx, req.Id, func(tx *store.Tx, o *store.Organization, p *store.Placement) error {
		return tx.BloggerReschedule(p, req.Body.PublishAt)
	})
	if err != nil {
		return nil, err
	}
	return api.BloggerReschedulePlacement200JSONResponse(out), nil
}

// ---------------------------------------------------------------- выплаты

func (h *Handlers) ListBloggerPayouts(ctx context.Context, req api.ListBloggerPayoutsRequestObject) (api.ListBloggerPayoutsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.Payout
	for _, po := range tx.D.Payouts {
		if po.OrgID == o.ID {
			mine = append(mine, po)
		}
	}
	mine = newestFirst(mine, func(po *store.Payout) time.Time { return po.CreatedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.Payout{}
	for _, po := range pg {
		item := api.Payout{Id: po.ID, PlacementId: po.PlacementID, Amount: po.Amount, Status: api.PayoutStatus(po.Status),
			Method: payoutMethod(o), Attempt: po.Attempt, ReceiptUrl: strPtr(po.ReceiptURL), CreatedAt: po.CreatedAt, CompletedAt: po.CompletedAt}
		if p, err := tx.Placement(po.PlacementID); err == nil {
			if c, err := tx.Channel(p.ChannelID); err == nil {
				item.Channel = channelRef(c)
			}
			if ord, err := tx.Order(p.OrderID); err == nil {
				item.OrderNumber = ord.Number
			}
		}
		items = append(items, item)
	}
	return api.ListBloggerPayouts200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

// payoutMethod — юрлицу переводом, остальным через Рокет Ворк (A-36).
func payoutMethod(o *store.Organization) api.PayoutMethod {
	if o.LegalType == "legal_entity" {
		return api.BankTransfer
	}
	return api.Rocketwork
}

func (h *Handlers) GetBloggerPayoutSummary(ctx context.Context, req api.GetBloggerPayoutSummaryRequestObject) (api.GetBloggerPayoutSummaryResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := api.GetBloggerPayoutSummary200JSONResponse{}
	for _, po := range tx.D.Payouts {
		if po.OrgID != o.ID {
			continue
		}
		out.Count++
		switch po.Status {
		case "succeeded":
			out.ReceivedAmount += po.Amount
		case "created", "sent":
			out.InProgressAmount += po.Amount
		}
	}
	return out, nil
}

func rocketworkStatus(tx *store.Tx, o *store.Organization) api.RocketworkStatus {
	out := api.RocketworkStatus{Status: "none", Required: o.LegalType != "legal_entity"}
	out.NextAction = strPtr("invite")
	if !out.Required {
		out.NextAction = strPtr("none")
	}
	if rw := tx.D.Rocketwork[o.ID]; rw != nil {
		out.Status = api.RocketworkContractorStatus(rw.Status)
		out.OnboardingUrl = strPtr(rw.OnboardingURL)
		switch rw.Status {
		case "active":
			out.NextAction = strPtr("none")
		case "blocked":
			out.NextAction = strPtr("grant_fns_permissions")
		case "invited", "pending":
			out.NextAction = strPtr("complete_registration")
		}
		if out.Status != "none" {
			at := tx.Now
			out.StatusCheckedAt = &at
		}
		if rw.Status == "blocked" {
			reason := "tax_status_lost"
			out.BlockedReason = &reason
		}
	}
	return out
}

func (h *Handlers) GetRocketworkStatus(ctx context.Context, req api.GetRocketworkStatusRequestObject) (api.GetRocketworkStatusResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	return api.GetRocketworkStatus200JSONResponse(rocketworkStatus(tx, o)), nil
}

func (h *Handlers) InviteRocketwork(ctx context.Context, req api.InviteRocketworkRequestObject) (api.InviteRocketworkResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	if o.LegalType == "legal_entity" {
		return nil, store.ConflictReason("contractor_not_required", "rocketwork is not required", nil)
	}
	rw := tx.D.Rocketwork[o.ID]
	if rw == nil {
		rw = &store.RocketworkContractor{}
		tx.D.Rocketwork[o.ID] = rw
	}
	if rw.Status != "active" {
		// Онбординг в Рокет Ворк заглушка проходит сразу.
		rw.Status = "active"
		rw.OnboardingURL = ""
		h.logger(ctx).Info("rocketwork contractor activated", "op", "Rocketwork.Invite", "organization_id", o.ID)
	}
	return api.InviteRocketwork202JSONResponse(rocketworkStatus(tx, o)), nil
}

// RefreshRocketworkStatus — в заглушке статус не меняется: возвращает текущий.
func (h *Handlers) RefreshRocketworkStatus(ctx context.Context, req api.RefreshRocketworkStatusRequestObject) (api.RefreshRocketworkStatusResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	if o.LegalType == "legal_entity" {
		return nil, store.ConflictReason("contractor_not_required", "rocketwork is not required", nil)
	}
	return api.RefreshRocketworkStatus200JSONResponse(rocketworkStatus(tx, o)), nil
}

func (h *Handlers) RequestRocketworkAction(ctx context.Context, req api.RequestRocketworkActionRequestObject) (api.RequestRocketworkActionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := blogger(ctx, tx)
	if err != nil {
		return nil, err
	}
	if o.LegalType == "legal_entity" {
		return nil, store.ConflictReason("contractor_not_required", "rocketwork is not required", nil)
	}
	if req.Body == nil {
		return nil, store.ConflictReason("invalid_action", "action is required", nil)
	}
	action := string(req.Body.Action)
	if action == "register" || action == "update" {
		if o.LegalType != "sole_proprietor" || req.Body.FirstName == nil || *req.Body.FirstName == "" || req.Body.LastName == nil || *req.Body.LastName == "" {
			return nil, store.ConflictReason("invalid_action", "entrepreneur names are required", nil)
		}
	}
	rw := tx.D.Rocketwork[o.ID]
	if rw == nil {
		rw = &store.RocketworkContractor{Status: "pending"}
		tx.D.Rocketwork[o.ID] = rw
	}
	if rw.Status != "active" {
		rw.Status = "pending"
	}
	rw.OnboardingURL = ""
	return api.RequestRocketworkAction202JSONResponse(rocketworkStatus(tx, o)), nil
}

// linkCodeAlphabet — символы кода подключения без похожих знаков (R-1).
const linkCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// linkCode — код подключения канала из 8 символов, как в бэкенде.
func linkCode() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = linkCodeAlphabet[rand.IntN(len(linkCodeAlphabet))]
	}
	return string(b)
}
