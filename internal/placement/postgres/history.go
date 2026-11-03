package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
)

// History — история состояний размещений (03 §12).
type History struct{ s *Store }

func toHistory(r sqlc.PlacementStateHistory) domain.HistoryEntry {
	return domain.HistoryEntry{
		ID: r.ID, PlacementID: r.PlacementID, Transition: domain.TransitionCode(r.Transition),
		From: domain.Status(str(r.FromStatus)), To: domain.Status(r.ToStatus), ActorType: domain.ActorType(r.ActorType),
		ActorUserID: r.ActorUserID, ReasonCode: str(r.ReasonCode), ReasonText: str(r.ReasonText), JobID: r.JobID, InboundEventID: r.InboundEventID,
		IdempotencyKey: str(r.IdempotencyKey), CreatedAt: r.CreatedAt,
	}
}

func (r *History) Insert(ctx context.Context, h domain.HistoryEntry) error {
	const op = "Placement.History.Insert"
	if err := r.s.q(ctx).InsertHistory(ctx, sqlc.InsertHistoryParams{
		ID: h.ID, PlacementID: h.PlacementID, Transition: string(h.Transition), FromStatus: text(string(h.From)),
		ToStatus: string(h.To), ActorType: string(h.ActorType), ActorUserID: h.ActorUserID, ReasonCode: text(h.ReasonCode),
		ReasonText: text(h.ReasonText), JobID: h.JobID, InboundEventID: h.InboundEventID, IdempotencyKey: text(h.IdempotencyKey), CreatedAt: h.CreatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err, "placement_id", h.PlacementID)
	}
	r.s.debug(ctx, op, "placement_id", h.PlacementID, "transition", h.Transition)
	return nil
}

func (r *History) ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID][]domain.HistoryEntry, error) {
	out := map[uuid.UUID][]domain.HistoryEntry{}
	if len(placementIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).ListHistoryByPlacements(ctx, placementIDs)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.History.ListByPlacements", err)
	}
	for _, row := range rows {
		out[row.PlacementID] = append(out[row.PlacementID], toHistory(row))
	}
	return out, nil
}

func (r *History) FindByKey(ctx context.Context, placementID uuid.UUID, key string) (domain.HistoryEntry, bool, error) {
	row, err := r.s.q(ctx).FindHistoryByKey(ctx, sqlc.FindHistoryByKeyParams{PlacementID: placementID, IdempotencyKey: &key})
	if err != nil {
		ok, err := found(r.s.fail(ctx, "Placement.History.FindByKey", err))
		return domain.HistoryEntry{}, ok, err
	}
	return toHistory(row), true, nil
}

func (r *History) ExistsKeyInOrder(ctx context.Context, orderID uuid.UUID, key string) (bool, error) {
	ok, err := r.s.q(ctx).ExistsHistoryKeyInOrder(ctx, sqlc.ExistsHistoryKeyInOrderParams{OrderID: orderID, IdempotencyKey: &key})
	if err != nil {
		return false, r.s.fail(ctx, "Placement.History.ExistsKeyInOrder", err)
	}
	return ok, nil
}
