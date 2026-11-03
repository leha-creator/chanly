package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Claims — претензии рекламодателя (04 §5, этап «Приёмка, споры и возвраты»).
type Claims struct{ s *Store }

// Claims возвращает репозиторий претензий.
func (s *Store) Claims() *Claims { return &Claims{s} }

var _ domain.Claims = (*Claims)(nil)

func toClaim(r sqlc.Claim) domain.Claim {
	return domain.Claim{ID: r.ID, PlacementID: r.PlacementID, Reason: domain.ClaimReason(r.Reason), Text: r.Text,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

func (r *Claims) Insert(ctx context.Context, c domain.Claim) error {
	const op = "Placement.Claims.Insert"
	err := r.s.q(ctx).InsertClaim(ctx, sqlc.InsertClaimParams{
		ID: c.ID, PlacementID: c.PlacementID, Reason: string(c.Reason), Text: c.Text, CreatedBy: c.CreatedBy,
		CreatedAt: c.CreatedAt,
	})
	if uniqueOn(err, "claim_placement_id_key") {
		r.s.log.DebugContext(ctx, "claim already exists", logx.KeyOp, op, "placement_id", c.PlacementID)
		return domain.ErrClaimExists
	}
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", c.PlacementID)
	}
	r.s.debug(ctx, op, "placement_id", c.PlacementID, "claim_id", c.ID, "reason", c.Reason)
	return nil
}

func (r *Claims) GetByPlacement(ctx context.Context, placementID uuid.UUID) (domain.Claim, error) {
	row, err := r.s.q(ctx).GetClaimByPlacement(ctx, placementID)
	if err != nil {
		return domain.Claim{}, r.s.fail(ctx, "Placement.Claims.GetByPlacement", err)
	}
	return toClaim(row), nil
}

func (r *Claims) ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]domain.Claim, error) {
	out := map[uuid.UUID]domain.Claim{}
	if len(placementIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).ListClaimsByPlacements(ctx, placementIDs)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Claims.ListByPlacements", err)
	}
	for _, row := range rows {
		out[row.PlacementID] = toClaim(row)
	}
	return out, nil
}

func (r *Disputes) GetByPlacementForUpdate(ctx context.Context, placementID uuid.UUID) (domain.Dispute, error) {
	row, err := r.s.q(ctx).GetDisputeByPlacementForUpdate(ctx, placementID)
	if err != nil {
		return domain.Dispute{}, r.s.fail(ctx, "Placement.Disputes.GetByPlacementForUpdate", err)
	}
	return toDispute(row), nil
}

func (r *Disputes) Resolve(ctx context.Context, d domain.Dispute) error {
	const op = "Placement.Disputes.Resolve"
	res := string(d.Resolution)
	comment := d.ResolutionComment
	n, err := r.s.q(ctx).ResolveDispute(ctx, sqlc.ResolveDisputeParams{
		ID: d.ID, Resolution: &res, PerformanceShare: int32Ptr(d.PerformanceShare), ResolutionComment: &comment,
		ResolvedBy: d.ResolvedBy, ResolvedAt: d.ResolvedAt, UpdatedAt: d.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err, "dispute_id", d.ID)
	}
	if n == 0 {
		r.s.log.WarnContext(ctx, "dispute already resolved", logx.KeyOp, op, "dispute_id", d.ID)
		return domain.ErrDisputeResolved
	}
	r.s.debug(ctx, op, "dispute_id", d.ID, "resolution", d.Resolution)
	return nil
}

func (r *Disputes) ListAdmin(ctx context.Context, f domain.DisputeFilter) ([]domain.Dispute, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.s.q(ctx).ListDisputesAdmin(ctx, sqlc.ListDisputesAdminParams{
		AllDisputes: f.All, OverdueAt: f.OverdueAt, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Disputes.ListAdmin", err)
	}
	out := make([]domain.Dispute, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDispute(row))
	}
	return out, nil
}

func (r *Violations) ListByPlacement(ctx context.Context, placementID uuid.UUID) ([]domain.Violation, error) {
	rows, err := r.s.q(ctx).ListViolationsByPlacement(ctx, placementID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Violations.ListByPlacement", err)
	}
	out := make([]domain.Violation, 0, len(rows))
	for _, row := range rows {
		v := domain.Violation{ID: row.ID, PlacementID: row.PlacementID, Kind: domain.ViolationKind(row.Kind),
			OccurredAt: row.OccurredAt, Source: domain.ViolationSource(row.Source), InboundEventID: row.InboundEventID,
			CreatedAt: row.CreatedAt}
		if len(row.Details) > 0 {
			_ = json.Unmarshal(row.Details, &v.Details)
		}
		out = append(out, v)
	}
	return out, nil
}
