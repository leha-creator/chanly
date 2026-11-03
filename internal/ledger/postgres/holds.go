package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func holdFromRow(r sqlc.Hold) domain.Hold {
	h := domain.Hold{
		ID: r.ID, PlacementID: r.PlacementID, PostVersionID: r.PostVersionID, OrganizationID: r.OrganizationID,
		Amount: money.Kopecks(r.Amount), Status: domain.HoldStatus(r.Status), CreatedAt: r.CreatedAt.UTC(),
	}
	if r.ClosedAt != nil {
		t := r.ClosedAt.UTC()
		h.ClosedAt = &t
	}
	return h
}

// ActiveHoldForUpdate — активное удержание размещения под блокировкой.
func (s *Store) ActiveHoldForUpdate(ctx context.Context, placementID uuid.UUID) (domain.Hold, error) {
	row, err := s.q(ctx).GetActiveHoldForUpdate(ctx, placementID)
	if err != nil {
		return domain.Hold{}, s.fail(ctx, "Ledger.ActiveHoldForUpdate", err)
	}
	return holdFromRow(row), nil
}

// ActiveHolds — активные удержания размещений.
func (s *Store) ActiveHolds(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]domain.Hold, error) {
	rows, err := s.q(ctx).ListActiveHolds(ctx, placementIDs)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.ActiveHolds", err)
	}
	out := make(map[uuid.UUID]domain.Hold, len(rows))
	for _, r := range rows {
		out[r.PlacementID] = holdFromRow(r)
	}
	return out, nil
}

// InsertHold записывает активное удержание.
func (s *Store) InsertHold(ctx context.Context, h domain.Hold) error {
	if err := s.q(ctx).InsertHold(ctx, sqlc.InsertHoldParams{
		ID: h.ID, PlacementID: h.PlacementID, PostVersionID: h.PostVersionID, OrganizationID: h.OrganizationID,
		Amount: int64(h.Amount), CreatedAt: h.CreatedAt,
	}); err != nil {
		return s.fail(ctx, "Ledger.InsertHold", err)
	}
	s.log.DebugContext(ctx, "hold saved", logx.KeyOp, "Ledger.InsertHold", "hold_id", h.ID, "placement_id", h.PlacementID)
	return nil
}

// CloseHold закрывает активное удержание.
func (s *Store) CloseHold(ctx context.Context, id uuid.UUID, status domain.HoldStatus, now time.Time) error {
	n, err := s.q(ctx).CloseHold(ctx, sqlc.CloseHoldParams{ID: id, Status: string(status), ClosedAt: &now})
	if err != nil {
		return s.fail(ctx, "Ledger.CloseHold", err)
	}
	if n == 0 {
		return fmt.Errorf("close hold %s: %w", id, domain.ErrNotFound)
	}
	s.log.DebugContext(ctx, "hold saved", logx.KeyOp, "Ledger.CloseHold", "hold_id", id, "hold_status", status)
	return nil
}

// HeldTotal — сумма активных удержаний рекламодателя.
func (s *Store) HeldTotal(ctx context.Context, orgID uuid.UUID) (money.Kopecks, error) {
	held, err := s.q(ctx).SumActiveHolds(ctx, orgID)
	if err != nil {
		return 0, s.fail(ctx, "Ledger.HeldTotal", err)
	}
	return money.Kopecks(held), nil
}
