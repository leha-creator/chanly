// Package httpapi — операции OpenAPI журнала проводок (тег balance): баланс рекламодателя и
// движения по нему (R-9).
package httpapi

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

const cabinetAdvertiser = "advertiser"

// Handlers — операции OpenAPI журнала.
type Handlers struct {
	svc   *app.Service
	log   *slog.Logger
	admin AdminPorts
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log.With(logx.KeyModule, "ledger")}
}

func advertiser(ctx context.Context) (uuid.UUID, error) {
	p, err := httpx.RequireCabinet(ctx, cabinetAdvertiser)
	if err != nil {
		return uuid.Nil, err
	}
	if p.OrganizationID == nil {
		return uuid.Nil, apperr.Forbidden(httpx.ReasonCabinetRequired).WithDetail("cabinet", cabinetAdvertiser)
	}
	return *p.OrganizationID, nil
}

func opt[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (h *Handlers) GetBalance(ctx context.Context, _ apigen.GetBalanceRequestObject) (apigen.GetBalanceResponseObject, error) {
	org, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	available, held, err := h.svc.Balance(ctx, org)
	if err != nil {
		return nil, err
	}
	return apigen.GetBalance200JSONResponse{AvailableAmount: int64(available), HeldAmount: int64(held)}, nil
}

func (h *Handlers) ListBalanceTransactions(ctx context.Context, req apigen.ListBalanceTransactionsRequestObject) (apigen.ListBalanceTransactionsResponseObject, error) {
	org, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.Operations(ctx, org, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.BalanceTransaction, 0, len(page.Items))
	for _, o := range page.Items {
		items = append(items, apigen.BalanceTransaction{
			Id: o.ID, Kind: apigen.BalanceTransactionKind(o.Kind), Amount: int64(o.Amount), Description: o.Description,
			OrderId: opt(o.OrderID), PlacementId: opt(o.PlacementID), InvoiceId: opt(o.InvoiceID), CreatedAt: o.CreatedAt,
		})
	}
	return apigen.ListBalanceTransactions200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}
