package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func (h *Handlers) MockReset(ctx context.Context, req api.MockResetRequestObject) (api.MockResetResponseObject, error) {
	h.st.Reset(ctx)
	return api.MockReset204Response{}, nil
}

func clockState(tx *store.Tx, off time.Duration, results []store.TimerResult) api.MockClock {
	out := api.MockClock{Now: tx.Now, OffsetHours: off.Hours(), Transitions: []api.MockTransition{}}
	for _, r := range results {
		out.Transitions = append(out.Transitions, api.MockTransition{
			PlacementId: uuid.MustParse(r.PlacementID), Transition: r.Transition, ToStatus: api.PlacementStatus(r.ToStatus)})
	}
	return out
}

func (h *Handlers) MockGetClock(ctx context.Context, req api.MockGetClockRequestObject) (api.MockGetClockResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	return api.MockGetClock200JSONResponse(clockState(tx, h.st.Clock().Offset(), nil)), nil
}

func (h *Handlers) MockShiftClock(ctx context.Context, req api.MockShiftClockRequestObject) (api.MockShiftClockResponseObject, error) {
	h.st.Clock().Shift(time.Duration(req.Body.Hours * float64(time.Hour)))
	tx, done := h.st.Begin(ctx) // Begin выполняет просроченные таймеры
	defer done()
	results := tx.Fired
	tx.Resync()
	h.logger(ctx).Info("mock clock shifted", "op", "Mock.Clock", "hours", req.Body.Hours, "now", tx.Now)
	return api.MockShiftClock200JSONResponse(clockState(tx, h.st.Clock().Offset(), results)), nil
}

func (h *Handlers) MockApproveOrderPost(ctx context.Context, req api.MockApproveOrderPostRequestObject) (api.MockApproveOrderPostResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	o, err := tx.Order(req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.ApprovePost(o); err != nil {
		return nil, err
	}
	return api.MockApproveOrderPost200JSONResponse(order(tx, o)), nil
}

func (h *Handlers) MockRejectOrderPost(ctx context.Context, req api.MockRejectOrderPostRequestObject) (api.MockRejectOrderPostResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	o, err := tx.Order(req.Id)
	if err != nil {
		return nil, err
	}
	reason := "Пост не прошёл модерацию"
	if req.Body != nil && req.Body.Reason != nil && *req.Body.Reason != "" {
		reason = *req.Body.Reason
	}
	if err := tx.RejectPost(o, reason); err != nil {
		return nil, err
	}
	return api.MockRejectOrderPost200JSONResponse(order(tx, o)), nil
}

func (h *Handlers) MockAdvancePlacement(ctx context.Context, req api.MockAdvancePlacementRequestObject) (api.MockAdvancePlacementResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	p, err := tx.Placement(req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.Advance(p, string(req.Body.Transition)); err != nil {
		return nil, err
	}
	return api.MockAdvancePlacement200JSONResponse(advertiserPlacement(tx, p)), nil
}

func (h *Handlers) MockPayInvoice(ctx context.Context, req api.MockPayInvoiceRequestObject) (api.MockPayInvoiceResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	var inv *store.Invoice
	for _, i := range tx.D.Invoices {
		if i.ID == req.Id {
			inv = i
		}
	}
	if inv == nil {
		return nil, store.NotFound("invoice", req.Id)
	}
	if inv.Status == "paid" || inv.Status == "cancelled" {
		return nil, store.Conflict("conflict", "invoice cannot be paid", map[string]any{"status": inv.Status})
	}
	// Просроченный счёт тоже зачисляется (S-06, отклонения).
	now, amount := tx.Now, inv.Amount
	inv.Status, inv.PaidAt, inv.PaidAmount = "paid", &now, &amount
	tx.Deposit(inv)
	h.logger(ctx).Info("invoice paid", "op", "Mock.PayInvoice", "invoice_id", inv.ID, "amount", amount)
	return api.MockPayInvoice200JSONResponse(invoice(inv)), nil
}

func (h *Handlers) MockLinkChannel(ctx context.Context, req api.MockLinkChannelRequestObject) (api.MockLinkChannelResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	i := slices.IndexFunc(tx.D.LinkCodes, func(lc *store.LinkCode) bool {
		return lc.Code == strings.ToUpper(strings.TrimSpace(req.Body.Code)) && tx.Now.Before(lc.ExpiresAt)
	})
	if i < 0 {
		return nil, store.NotFound("channel_link_code", "code")
	}
	lc := tx.D.LinkCodes[i]
	tx.D.LinkCodes = slices.Delete(tx.D.LinkCodes, i, i+1)
	title := "Подключённый тестовый канал"
	if req.Body.Title != nil && *req.Body.Title != "" {
		title = *req.Body.Title
	}
	subs := 3000
	if req.Body.SubscribersCount != nil {
		subs = *req.Body.SubscribersCount
	}
	org := lc.OrgID
	tx.D.NextMaxChatID--
	c := &store.Channel{
		ID: store.NewID(), OrgID: &org, MaxChatID: tx.D.NextMaxChatID, Title: title,
		MaxDescription: "Описание канала из MAX (выдуманное)", IconURL: "https://placehold.co/128x128/png?text=new",
		Link: fmt.Sprintf("https://max.ru/test_channel_%d", -tx.D.NextMaxChatID), IsPublic: true, Status: "moderation",
		BotIsAdmin: true, BotPermissions: []string{"read", "write", "delete", "pin"}, Subscribers: subs, AvgViews: subs / 3,
		PostsPerWeek: 7, RknStatus: "not_required", Rating: 100, Prices: map[string]*store.Price{}, CreatedAt: tx.Now,
	}
	if subs >= 10000 {
		c.RknStatus = "pending"
	}
	tx.D.Channels = append(tx.D.Channels, c)
	tx.RecheckChannel(c)
	h.logger(ctx).Info("channel linked", "op", "Mock.LinkChannel", "channel_id", c.ID, "organization_id", org)
	return api.MockLinkChannel201JSONResponse(bloggerChannel(tx, c)), nil
}

// ---------------------------------------------------------------- webhooks

func (h *Handlers) MaxWebhook(ctx context.Context, req api.MaxWebhookRequestObject) (api.MaxWebhookResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	mockStartConnection(tx, *req.Body)
	h.logger(ctx).Info("max webhook received and ignored", "op", "Webhook.Max", "keys", len(*req.Body))
	return api.MaxWebhook200Response{}, nil
}

func (h *Handlers) RocketworkWebhook(ctx context.Context, req api.RocketworkWebhookRequestObject) (api.RocketworkWebhookResponseObject, error) {
	h.logger(ctx).Info("rocketwork webhook received and ignored", "op", "Webhook.Rocketwork", "keys", len(*req.Body))
	return api.RocketworkWebhook200Response{}, nil
}
