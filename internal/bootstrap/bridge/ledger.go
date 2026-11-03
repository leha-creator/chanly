package bridge

import (
	"context"
	"errors"

	"github.com/google/uuid"

	billingpg "github.com/leha-creator/chanly/internal/billing/postgres"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/placement/domain"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
)

// ---------------------------------------------------------------- placement ← ledger

// LedgerHolds — placement/domain.Holds поверх журнала проводок (R-6 … R-8): нехватка баланса
// журнала переводится в placement/domain.InsufficientBalanceError.
type LedgerHolds struct{ Svc *ledgerapp.Service }

var _ domain.Holds = LedgerHolds{}

func (h LedgerHolds) HoldAll(ctx context.Context, orgID uuid.UUID, reqs []domain.HoldRequest) error {
	out := make([]ledgerapp.HoldRequest, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, ledgerapp.HoldRequest{PlacementID: r.PlacementID, PostVersionID: r.PostVersionID, Amount: r.Amount})
	}
	err := h.Svc.HoldAll(ctx, orgID, out)
	var neg *ledgerdomain.NegativeLiabilityError
	if errors.As(err, &neg) {
		return &domain.InsufficientBalanceError{Missing: neg.Missing}
	}
	return err
}

func (h LedgerHolds) Release(ctx context.Context, placementID uuid.UUID, reason string) error {
	return h.Svc.Release(ctx, placementID, reason)
}

func (h LedgerHolds) HasActive(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return h.Svc.HasActive(ctx, placementIDs)
}

// Settle — распределение удержания; актор проводки settlement — по переходу (R-6 этапа приёмки).
func (h LedgerHolds) Settle(ctx context.Context, placementID, bloggerOrgID uuid.UUID, d domain.Distribution, by domain.Settler) error {
	actor, user := ledgerdomain.ActorSystem, uuid.Nil
	switch by.Actor {
	case domain.ActorAdvertiser:
		actor = ledgerdomain.ActorAdvertiser
	case domain.ActorAdmin:
		actor = ledgerdomain.ActorAdmin
	}
	if by.UserID != nil && actor != ledgerdomain.ActorSystem {
		user = *by.UserID
	}
	return h.Svc.Settle(ctx, placementID, bloggerOrgID, ledgerdomain.Distribution{
		Payout: d.PayoutAmount, FeeCharged: d.FeeCharged, Refund: d.RefundAmount,
	}, actor, user)
}

// ---------------------------------------------------------------- ledger ← placement, billing

// LedgerReferences — номера заказов и счетов для описаний операций баланса (R-9).
type LedgerReferences struct {
	Placements *placementpg.Placements
	Invoices   *billingpg.Store
}

var _ ledgerapp.References = LedgerReferences{}

func (r LedgerReferences) Orders(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]ledgerapp.OrderRef, error) {
	refs, err := r.Placements.Refs(ctx, placementIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]ledgerapp.OrderRef, len(refs))
	for id, ref := range refs {
		out[id] = ledgerapp.OrderRef{OrderID: ref.OrderID, OrderNumber: ref.OrderNumber}
	}
	return out, nil
}

func (r LedgerReferences) InvoiceNumbers(ctx context.Context, invoiceIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	return r.Invoices.InvoiceNumbers(ctx, invoiceIDs)
}
