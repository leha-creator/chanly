package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Publications — публикации (04 §5).
type Publications struct{ s *Store }

// Violations — нарушения (04 §5). Только добавление (R-20).
type Violations struct{ s *Store }

// Disputes — споры (04 §5).
type Disputes struct{ s *Store }

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// uniqueOn — нарушение уникальности именно этого ограничения.
func uniqueOn(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

func toPublication(r sqlc.Publication) domain.Publication {
	return domain.Publication{
		ID: r.ID, PlacementID: r.PlacementID, ChannelID: r.ChannelID, MaxMessageID: r.MaxMessageID, URL: str(r.Url),
		PublishedAt: r.PublishedAt, RemovedAt: r.RemovedAt, RemovalReason: domain.RemovalReason(str(r.RemovalReason)),
		RemovalOverdue: r.RemovalOverdue, Views: intPtr(r.Views), ViewsCheckedAt: r.ViewsCheckedAt,
		ContentHash: r.ContentHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (r *Publications) Insert(ctx context.Context, pub domain.Publication) error {
	const op = "Placement.Publications.Insert"
	err := r.s.q(ctx).InsertPublication(ctx, sqlc.InsertPublicationParams{
		ID: pub.ID, PlacementID: pub.PlacementID, ChannelID: pub.ChannelID, MaxMessageID: pub.MaxMessageID,
		Url: text(pub.URL), PublishedAt: pub.PublishedAt, RemovedAt: pub.RemovedAt, RemovalReason: text(string(pub.RemovalReason)),
		RemovalOverdue: pub.RemovalOverdue, Views: int32Ptr(pub.Views), ViewsCheckedAt: pub.ViewsCheckedAt,
		ContentHash: pub.ContentHash, CreatedAt: pub.CreatedAt, UpdatedAt: pub.UpdatedAt,
	})
	if uniqueOn(err, "publication_placement_id_key") {
		return domain.ErrPublicationExists
	}
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", pub.PlacementID)
	}
	r.s.log.DebugContext(ctx, "publication saved", logx.KeyOp, op, "publication_id", pub.ID, "placement_id", pub.PlacementID,
		"removed", false, "removal_overdue", pub.RemovalOverdue)
	return nil
}

func (r *Publications) GetByPlacement(ctx context.Context, placementID uuid.UUID) (domain.Publication, error) {
	row, err := r.s.q(ctx).GetPublicationByPlacement(ctx, placementID)
	if err != nil {
		return domain.Publication{}, r.s.fail(ctx, "Placement.Publications.GetByPlacement", err)
	}
	return toPublication(row), nil
}

func (r *Publications) GetByPlacementForUpdate(ctx context.Context, placementID uuid.UUID) (domain.Publication, error) {
	row, err := r.s.q(ctx).GetPublicationByPlacementForUpdate(ctx, placementID)
	if err != nil {
		return domain.Publication{}, r.s.fail(ctx, "Placement.Publications.GetByPlacementForUpdate", err)
	}
	return toPublication(row), nil
}

func (r *Publications) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Publication, error) {
	row, err := r.s.q(ctx).GetPublicationForUpdate(ctx, id)
	if err != nil {
		return domain.Publication{}, r.s.fail(ctx, "Placement.Publications.GetForUpdate", err)
	}
	return toPublication(row), nil
}

func (r *Publications) FindByMessageID(ctx context.Context, messageID string) (domain.Publication, bool, error) {
	row, err := r.s.q(ctx).GetPublicationByMessageID(ctx, messageID)
	if err != nil {
		ok, err := found(r.s.fail(ctx, "Placement.Publications.FindByMessageID", err))
		return domain.Publication{}, ok, err
	}
	return toPublication(row), true, nil
}

func (r *Publications) Update(ctx context.Context, pub domain.Publication) error {
	const op = "Placement.Publications.Update"
	if err := r.s.q(ctx).UpdatePublication(ctx, sqlc.UpdatePublicationParams{
		ID: pub.ID, RemovedAt: pub.RemovedAt, RemovalReason: text(string(pub.RemovalReason)), RemovalOverdue: pub.RemovalOverdue,
		Views: int32Ptr(pub.Views), ViewsCheckedAt: pub.ViewsCheckedAt, UpdatedAt: pub.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err, "publication_id", pub.ID)
	}
	r.s.log.DebugContext(ctx, "publication saved", logx.KeyOp, op, "publication_id", pub.ID, "removed", pub.RemovedAt != nil,
		"removal_overdue", pub.RemovalOverdue)
	return nil
}

func (r *Publications) ListLive(ctx context.Context, after uuid.UUID, limit int) ([]domain.Publication, error) {
	rows, err := r.s.q(ctx).ListLivePublications(ctx, sqlc.ListLivePublicationsParams{After: after, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Publications.ListLive", err)
	}
	out := make([]domain.Publication, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPublication(row))
	}
	return out, nil
}

func (r *Publications) ListLiveByChannel(ctx context.Context, channelID uuid.UUID) ([]domain.LivePlacement, error) {
	rows, err := r.s.q(ctx).ListLivePublicationsByChannel(ctx, channelID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Publications.ListLiveByChannel", err)
	}
	out := make([]domain.LivePlacement, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.LivePlacement{Placement: toPlacement(row.Placement), Publication: toPublication(row.Publication)})
	}
	return out, nil
}

func (r *Publications) ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]domain.Publication, error) {
	out := map[uuid.UUID]domain.Publication{}
	if len(placementIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).ListPublicationsByPlacements(ctx, placementIDs)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Publications.ListByPlacements", err)
	}
	for _, row := range rows {
		out[row.PlacementID] = toPublication(row)
	}
	return out, nil
}

func (r *Publications) OverdueRemovals(ctx context.Context, before time.Time) ([]uuid.UUID, error) {
	ids, err := r.s.q(ctx).OverdueRemovals(ctx, before)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Publications.OverdueRemovals", err)
	}
	return ids, nil
}

func (r *Violations) Insert(ctx context.Context, v domain.Violation) error {
	const op = "Placement.Violations.Insert"
	details := v.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	n, err := r.s.q(ctx).InsertViolation(ctx, sqlc.InsertViolationParams{
		ID: v.ID, PlacementID: v.PlacementID, Kind: string(v.Kind), OccurredAt: v.OccurredAt, Source: string(v.Source),
		InboundEventID: v.InboundEventID, Details: raw, CreatedAt: v.CreatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", v.PlacementID)
	}
	if n == 0 {
		return domain.ErrDuplicateViolation
	}
	r.s.log.DebugContext(ctx, "violation saved", logx.KeyOp, op, "placement_id", v.PlacementID, "kind", v.Kind)
	return nil
}

func toDispute(r sqlc.Dispute) domain.Dispute {
	return domain.Dispute{
		ID: r.ID, PlacementID: r.PlacementID, Reason: r.Reason, OpenedBy: domain.OpenedBy(r.OpenedByType),
		ClaimID: r.ClaimID, ViolationID: r.ViolationID, DeadlineAt: r.DeadlineAt, SuggestedShare: intPtr(r.SuggestedShare),
		Resolution: domain.Resolution(str(r.Resolution)), PerformanceShare: intPtr(r.PerformanceShare),
		ResolutionComment: str(r.ResolutionComment), ResolvedBy: r.ResolvedBy, ResolvedAt: r.ResolvedAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (r *Disputes) Insert(ctx context.Context, d domain.Dispute) error {
	const op = "Placement.Disputes.Insert"
	err := r.s.q(ctx).InsertDispute(ctx, sqlc.InsertDisputeParams{
		ID: d.ID, PlacementID: d.PlacementID, Reason: d.Reason, OpenedByType: string(d.OpenedBy), ClaimID: d.ClaimID, ViolationID: d.ViolationID,
		DeadlineAt: d.DeadlineAt, SuggestedShare: int32Ptr(d.SuggestedShare), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	})
	if uniqueOn(err, "dispute_placement_id_key") {
		return domain.ErrDisputeExists
	}
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", d.PlacementID)
	}
	r.s.debug(ctx, op, "placement_id", d.PlacementID, "reason", d.Reason)
	return nil
}

func (r *Disputes) GetByPlacement(ctx context.Context, placementID uuid.UUID) (domain.Dispute, error) {
	row, err := r.s.q(ctx).GetDisputeByPlacement(ctx, placementID)
	if err != nil {
		return domain.Dispute{}, r.s.fail(ctx, "Placement.Disputes.GetByPlacement", err)
	}
	return toDispute(row), nil
}

func (r *Disputes) ListByPlacements(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]domain.Dispute, error) {
	out := map[uuid.UUID]domain.Dispute{}
	if len(placementIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).ListDisputesByPlacements(ctx, placementIDs)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Disputes.ListByPlacements", err)
	}
	for _, row := range rows {
		out[row.PlacementID] = toDispute(row)
	}
	return out, nil
}
