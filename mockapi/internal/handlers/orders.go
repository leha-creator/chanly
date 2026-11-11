package handlers

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

func timing(p *store.Placement) api.PlacementTiming {
	return api.PlacementTiming{
		FormatId: p.FormatID, TopHours: p.TopHours, DurationHours: p.DurationHours,
		WindowStart: p.WindowStart, WindowEnd: p.WindowEnd, TimeFrom: p.TimeFrom, TimeTo: p.TimeTo,
		AcceptDeadlineAt: p.AcceptDeadlineAt, ScheduleDeadlineAt: p.ScheduleDeadlineAt,
		PublishAt: p.PublishAt, RemoveAt: p.RemoveAt, PublishedAt: p.PublishedAt, RemovedAt: p.RemovedAt,
		ClaimDeadlineAt: p.ClaimDeadlineAt,
	}
}

func claimOpen(tx *store.Tx, p *store.Placement) bool {
	if p.Claim != nil {
		return false
	}
	return p.Status == "published" || (p.Status == "removed" && p.ClaimDeadlineAt != nil && tx.Now.Before(*p.ClaimDeadlineAt))
}

func advertiserPlacement(tx *store.Tx, p *store.Placement) api.AdvertiserPlacement {
	t := timing(p)
	out := api.AdvertiserPlacement{
		Id: p.ID, OrderId: p.OrderID, Status: api.PlacementStatus(p.Status), StatusReason: strPtr(p.StatusReason),
		StatusChangedAt: p.StatusChangedAt, TotalAmount: p.TotalAmount, RefundAmount: p.RefundAmount,
		PerformanceShare: p.PerformanceShare, PublicationUrl: strPtr(p.PublicationURL), Views: p.Views,
		History: []api.PlacementHistoryEntry{}, AvailableActions: []api.AdvertiserPlacementAction{},
		FormatId: t.FormatId, TopHours: t.TopHours, DurationHours: t.DurationHours, WindowStart: t.WindowStart,
		WindowEnd: t.WindowEnd, TimeFrom: t.TimeFrom, TimeTo: t.TimeTo, AcceptDeadlineAt: t.AcceptDeadlineAt,
		ScheduleDeadlineAt: t.ScheduleDeadlineAt, PublishAt: t.PublishAt, RemoveAt: t.RemoveAt,
		PublishedAt: t.PublishedAt, RemovedAt: t.RemovedAt, ClaimDeadlineAt: t.ClaimDeadlineAt,
	}
	if c, err := tx.Channel(p.ChannelID); err == nil {
		out.Channel = channelRef(c)
	}
	if p.Claim != nil {
		out.Claim = &api.PlacementClaim{Reason: api.ClaimReason(p.Claim.Reason), Text: p.Claim.Text, CreatedAt: p.Claim.CreatedAt}
	}
	out.Dispute = disputeSummary(p)
	for _, e := range p.History {
		h := api.PlacementHistoryEntry{Transition: e.Transition, ToStatus: api.PlacementStatus(e.To),
			ActorType: api.PlacementHistoryEntryActorType(e.ActorType), ReasonCode: strPtr(e.ReasonCode), CreatedAt: e.CreatedAt}
		if e.From != "" {
			h.FromStatus = ptr(api.PlacementStatus(e.From))
		}
		out.History = append(out.History, h)
	}
	if store.Allowed("P-06", p.Status) {
		out.AvailableActions = append(out.AvailableActions, "cancel")
	}
	if store.Allowed("P-12", p.Status) {
		out.AvailableActions = append(out.AvailableActions, "accept")
	}
	if claimOpen(tx, p) {
		out.AvailableActions = append(out.AvailableActions, "claim")
	}
	return out
}

func postVersion(tx *store.Tx, orgID uuid.UUID, v *store.PostVersion) api.PostVersion {
	urls := versionMediaURLs(tx, orgID, v.MediaIDs, v.TokenCarrier)
	return api.PostVersion{
		Id: v.ID, Number: v.Number, Status: api.PostVersionStatus(v.Status), Body: strPtr(v.Body),
		FinalText: strPtr(v.FinalText), Erid: strPtr(v.Erid), MediaUrls: &urls,
		TokenCarrier: api.TokenCarrier(v.TokenCarrier), TokenPosition: api.TokenPosition(v.TokenPosition),
		RejectionReason: strPtr(v.RejectionReason), CreatedAt: v.CreatedAt,
	}
}

func orderSummary(tx *store.Tx, o *store.Order) api.OrderSummary {
	ps := tx.OrderPlacements(o.ID)
	by := map[string]int{}
	for _, p := range ps {
		by[p.Status]++
	}
	name := ""
	for _, c := range tx.D.Campaigns {
		if c.ID == o.CampaignID {
			name = c.Name
		}
	}
	return api.OrderSummary{Id: o.ID, Number: o.Number, CampaignId: o.CampaignID, CampaignName: name,
		Status: api.OrderStatus(o.Status), TotalAmount: o.TotalAmount, PlacementsCount: len(ps),
		PlacementsByStatus: by, CreatedAt: o.CreatedAt}
}

func order(tx *store.Tx, o *store.Order) api.Order {
	s := orderSummary(tx, o)
	out := api.Order{Id: s.Id, Number: s.Number, CampaignId: s.CampaignId, CampaignName: s.CampaignName,
		Status: s.Status, TotalAmount: s.TotalAmount, PlacementsCount: s.PlacementsCount,
		PlacementsByStatus: s.PlacementsByStatus, CreatedAt: s.CreatedAt,
		CurrentPostVersion: postVersion(tx, o.OrgID, o.CurrentVersion()), Placements: []api.AdvertiserPlacement{}}
	for _, p := range tx.OrderPlacements(o.ID) {
		out.Placements = append(out.Placements, advertiserPlacement(tx, p))
	}
	return out
}

func (h *Handlers) ListOrders(ctx context.Context, req api.ListOrdersRequestObject) (api.ListOrdersResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.Order
	for _, x := range tx.D.Orders {
		if x.OrgID == o.ID && (req.Params.Status == nil || string(*req.Params.Status) == x.Status) {
			mine = append(mine, x)
		}
	}
	mine = newestFirst(mine, func(x *store.Order) time.Time { return x.CreatedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.OrderSummary{}
	for _, x := range pg {
		items = append(items, orderSummary(tx, x))
	}
	return api.ListOrders200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetOrder(ctx context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	x, err := tx.OrgOrder(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetOrder200JSONResponse(order(tx, x)), nil
}

func (h *Handlers) CreateOrderPostVersion(ctx context.Context, req api.CreateOrderPostVersionRequestObject) (api.CreateOrderPostVersionResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	x, err := tx.OrgOrder(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	c, err := tx.Campaign(o.ID, x.CampaignID)
	if err != nil {
		return nil, err
	}
	if err := tx.NewPostVersion(o, x, c); err != nil {
		return nil, err
	}
	return api.CreateOrderPostVersion201JSONResponse(order(tx, x)), nil
}

func (h *Handlers) CancelOrder(ctx context.Context, req api.CancelOrderRequestObject) (api.CancelOrderResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	x, err := tx.OrgOrder(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.CancelOrder(x); err != nil {
		return nil, err
	}
	return api.CancelOrder200JSONResponse(order(tx, x)), nil
}

// advPlacement находит размещение заказа рекламодателя.
func advPlacement(tx *store.Tx, orgID, id uuid.UUID) (*store.Placement, error) {
	p, err := tx.Placement(id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.OrgOrder(orgID, p.OrderID); err != nil {
		return nil, store.NotFound("placement", id)
	}
	return p, nil
}

func (h *Handlers) placementAction(ctx context.Context, id uuid.UUID, act func(*store.Tx, *store.Placement) error) (api.AdvertiserPlacement, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return api.AdvertiserPlacement{}, err
	}
	p, err := advPlacement(tx, o.ID, id)
	if err != nil {
		return api.AdvertiserPlacement{}, err
	}
	if err := act(tx, p); err != nil {
		return api.AdvertiserPlacement{}, err
	}
	return advertiserPlacement(tx, p), nil
}

func (h *Handlers) CancelPlacement(ctx context.Context, req api.CancelPlacementRequestObject) (api.CancelPlacementResponseObject, error) {
	out, err := h.placementAction(ctx, req.Id, func(tx *store.Tx, p *store.Placement) error { return tx.CancelPlacement(p) })
	if err != nil {
		return nil, err
	}
	return api.CancelPlacement200JSONResponse(out), nil
}

func (h *Handlers) AcceptPlacement(ctx context.Context, req api.AcceptPlacementRequestObject) (api.AcceptPlacementResponseObject, error) {
	out, err := h.placementAction(ctx, req.Id, func(tx *store.Tx, p *store.Placement) error { return tx.AcceptPlacement(p) })
	if err != nil {
		return nil, err
	}
	return api.AcceptPlacement200JSONResponse(out), nil
}

func (h *Handlers) CreatePlacementClaim(ctx context.Context, req api.CreatePlacementClaimRequestObject) (api.CreatePlacementClaimResponseObject, error) {
	out, err := h.placementAction(ctx, req.Id, func(tx *store.Tx, p *store.Placement) error {
		return tx.ClaimPlacement(p, string(req.Body.Reason), req.Body.Text)
	})
	if err != nil {
		return nil, err
	}
	return api.CreatePlacementClaim201JSONResponse(out), nil
}

// ---------------------------------------------------------------- баланс

func (h *Handlers) GetBalance(ctx context.Context, req api.GetBalanceRequestObject) (api.GetBalanceResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	available, held := tx.Balance(o.ID)
	return api.GetBalance200JSONResponse{AvailableAmount: max(available, 0), HeldAmount: held}, nil
}

func (h *Handlers) ListBalanceTransactions(ctx context.Context, req api.ListBalanceTransactionsRequestObject) (api.ListBalanceTransactionsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.LedgerEntry
	for _, e := range tx.D.Ledger {
		if e.OrgID == o.ID {
			mine = append(mine, e)
		}
	}
	mine = newestFirst(mine, func(e *store.LedgerEntry) time.Time { return e.CreatedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.BalanceTransaction{}
	for _, e := range pg {
		items = append(items, api.BalanceTransaction{Id: e.ID, Kind: api.BalanceTransactionKind(e.Kind), Amount: e.Amount,
			Description: e.Description, OrderId: e.OrderID, PlacementId: e.PlacementID, InvoiceId: e.InvoiceID, CreatedAt: e.CreatedAt})
	}
	return api.ListBalanceTransactions200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func invoice(i *store.Invoice) api.Invoice {
	return api.Invoice{Id: i.ID, Number: i.Number, Amount: i.Amount, Status: api.InvoiceStatus(i.Status),
		ExpiresAt: i.ExpiresAt, PaidAt: i.PaidAt, PaidAmount: i.PaidAmount, CreatedAt: i.CreatedAt,
		PdfUrl: fmt.Sprintf("/api/v1/invoices/%s/pdf", i.ID)}
}

func (h *Handlers) ListInvoices(ctx context.Context, req api.ListInvoicesRequestObject) (api.ListInvoicesResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	var mine []*store.Invoice
	for _, i := range tx.D.Invoices {
		if i.OrgID == o.ID {
			mine = append(mine, i)
		}
	}
	mine = newestFirst(mine, func(i *store.Invoice) time.Time { return i.CreatedAt })
	pg, next, total := page(mine, req.Params.Limit, req.Params.Cursor)
	items := []api.Invoice{}
	for _, i := range pg {
		items = append(items, invoice(i))
	}
	return api.ListInvoices200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) CreateInvoice(ctx context.Context, req api.CreateInvoiceRequestObject) (api.CreateInvoiceResponseObject, error) {
	// The mock models ordinary business limits; controlled real-bank tests use
	// the backend with its explicit organization-bound admission.
	if req.Body.Amount < 100000 || req.Body.Amount > 100000000000 {
		return nil, store.Validation("amount", "out_of_range")
	}
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	i := &store.Invoice{ID: store.NewID(), Number: fmt.Sprintf("MX-%06d", tx.D.NextInvoiceNo),
		OrgID: o.ID, Amount: req.Body.Amount, Status: "issued", ExpiresAt: tx.Now.Add(14 * 24 * time.Hour), CreatedAt: tx.Now}
	tx.D.NextInvoiceNo++
	tx.D.Invoices = append(tx.D.Invoices, i)
	h.logger(ctx).Info("invoice issued", "op", "Invoice.Create", "organization_id", o.ID, "invoice_id", i.ID, "amount", i.Amount)
	return api.CreateInvoice201JSONResponse(invoice(i)), nil
}

func (h *Handlers) GetInvoicePdf(ctx context.Context, req api.GetInvoicePdfRequestObject) (api.GetInvoicePdfResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	_, o, err := advertiser(ctx, tx)
	if err != nil {
		return nil, err
	}
	i, err := tx.Invoice(o.ID, req.Id)
	if err != nil {
		return nil, err
	}
	// В PDF только латиница: шрифт Helvetica без кириллицы.
	pdf := invoicePDF(fmt.Sprintf("TEST INVOICE %s. Amount: %d.%02d RUB. Not a real document.",
		i.Number, i.Amount/100, i.Amount%100))
	return api.GetInvoicePdf200ApplicationpdfResponse{Body: bytes.NewReader(pdf), ContentLength: int64(len(pdf))}, nil
}

// invoicePDF собирает одностраничный PDF с одной строкой латиницей.
func invoicePDF(line string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	stream := fmt.Sprintf("BT /F1 14 Tf 50 780 Td (%s) Tj ET", line)
	objs[3] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}
