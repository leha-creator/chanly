package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Placements — размещения.
type Placements struct{ s *Store }

func kopecks(p *int64) *money.Kopecks {
	if p == nil {
		return nil
	}
	v := money.Kopecks(*p)
	return &v
}

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func toPlacement(r sqlc.Placement) domain.Placement {
	return domain.Placement{
		ID: r.ID, OrderID: r.OrderID, ChannelID: r.ChannelID, BloggerOrgID: r.BloggerOrganizationID,
		FormatID: r.FormatID, TopHours: int(r.TopHours), DurationHours: int(r.DurationHours),
		WindowStart: r.WindowStart, WindowEnd: r.WindowEnd, TimeFrom: fromPgTime(r.TimeFrom), TimeTo: fromPgTime(r.TimeTo),
		PostVersionID: r.PostVersionID, Status: domain.Status(r.Status), StatusChangedAt: r.StatusChangedAt,
		StatusReason: str(r.StatusReason), SentToBloggerAt: r.SentToBloggerAt, FeeRateBP: int(r.FeeRateBp),
		BloggerAmount: money.Kopecks(r.BloggerAmount), FeeAmount: money.Kopecks(r.FeeAmount),
		TotalAmount: money.Kopecks(r.TotalAmount), AcceptDeadlineAt: r.AcceptDeadlineAt,
		ScheduleDeadlineAt: r.ScheduleDeadlineAt, PublishAt: r.PublishAt, RemoveAt: r.RemoveAt,
		PublishedAt: r.PublishedAt, RemovedAt: r.RemovedAt, ClaimDeadlineAt: r.ClaimDeadlineAt,
		PerformanceShare: intPtr(r.PerformanceShare), PayoutAmount: kopecks(r.PayoutAmount),
		FeeCharged: kopecks(r.FeeCharged), RefundAmount: kopecks(r.RefundAmount), FinalViews: intPtr(r.FinalViews),
		LockVersion: int(r.LockVersion), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toPlacements(rows []sqlc.Placement) []domain.Placement {
	out := make([]domain.Placement, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPlacement(row))
	}
	return out
}

func (r *Placements) Insert(ctx context.Context, p domain.Placement) error {
	const op = "Placement.Placements.Insert"
	if err := r.s.q(ctx).InsertPlacement(ctx, sqlc.InsertPlacementParams{
		ID: p.ID, OrderID: p.OrderID, ChannelID: p.ChannelID, BloggerOrganizationID: p.BloggerOrgID,
		FormatID: p.FormatID, TopHours: int32(p.TopHours), DurationHours: int32(p.DurationHours),
		WindowStart: p.WindowStart, WindowEnd: p.WindowEnd, TimeFrom: pgTime(p.TimeFrom), TimeTo: pgTime(p.TimeTo),
		PostVersionID: p.PostVersionID, Status: string(p.Status), StatusChangedAt: p.StatusChangedAt,
		StatusReason: text(p.StatusReason), SentToBloggerAt: p.SentToBloggerAt, FeeRateBp: int32(p.FeeRateBP),
		BloggerAmount: int64(p.BloggerAmount), FeeAmount: int64(p.FeeAmount), TotalAmount: int64(p.TotalAmount),
		AcceptDeadlineAt: p.AcceptDeadlineAt, ScheduleDeadlineAt: p.ScheduleDeadlineAt, PublishAt: p.PublishAt,
		RemoveAt: p.RemoveAt, LockVersion: int32(p.LockVersion), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err, "placement_id", p.ID)
	}
	r.s.log.DebugContext(ctx, "placement saved", logx.KeyOp, op, "placement_id", p.ID, "status", p.Status, "lock_version", p.LockVersion)
	return nil
}

func (r *Placements) Get(ctx context.Context, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetPlacement(ctx, id)
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.Get", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) GetForAdvertiser(ctx context.Context, orgID, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetAdvertiserPlacement(ctx, sqlc.GetAdvertiserPlacementParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.GetForAdvertiser", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetPlacementForUpdate(ctx, id)
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.GetForUpdate", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) GetForAdvertiserForUpdate(ctx context.Context, orgID, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetAdvertiserPlacementForUpdate(ctx, sqlc.GetAdvertiserPlacementForUpdateParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.GetForAdvertiserForUpdate", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) GetForBlogger(ctx context.Context, orgID, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetBloggerPlacement(ctx, sqlc.GetBloggerPlacementParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.GetForBlogger", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) GetForBloggerForUpdate(ctx context.Context, orgID, id uuid.UUID) (domain.Placement, error) {
	row, err := r.s.q(ctx).GetBloggerPlacementForUpdate(ctx, sqlc.GetBloggerPlacementForUpdateParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Placement{}, r.s.fail(ctx, "Placement.Placements.GetForBloggerForUpdate", err)
	}
	return toPlacement(row), nil
}

func (r *Placements) UpdateIfStatus(ctx context.Context, p domain.Placement, status domain.Status, lockVersion int) error {
	const op = "Placement.Placements.UpdateIfStatus"
	n, err := r.s.q(ctx).UpdatePlacement(ctx, sqlc.UpdatePlacementParams{
		ID: p.ID, PostVersionID: p.PostVersionID, Status: string(p.Status), StatusChangedAt: p.StatusChangedAt,
		StatusReason: text(p.StatusReason), SentToBloggerAt: p.SentToBloggerAt, AcceptDeadlineAt: p.AcceptDeadlineAt,
		ScheduleDeadlineAt: p.ScheduleDeadlineAt, PublishAt: p.PublishAt, RemoveAt: p.RemoveAt,
		PublishedAt: p.PublishedAt, RemovedAt: p.RemovedAt, ClaimDeadlineAt: p.ClaimDeadlineAt,
		FinalViews: int32Ptr(p.FinalViews), LockVersion: int32(p.LockVersion), UpdatedAt: p.UpdatedAt,
		PrevStatus: string(status), PrevLockVersion: int32(lockVersion),
	})
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", p.ID)
	}
	if n == 0 {
		r.s.log.WarnContext(ctx, "placement update skipped, stale state", logx.KeyOp, op, "placement_id", p.ID,
			"expected_status", status, "expected_lock_version", lockVersion)
		return domain.ErrStaleVersion
	}
	r.s.log.DebugContext(ctx, "placement saved", logx.KeyOp, op, "placement_id", p.ID, "status", p.Status, "lock_version", p.LockVersion)
	return nil
}

func (r *Placements) SetDistribution(ctx context.Context, id uuid.UUID, d domain.Distribution) error {
	const op = "Placement.Placements.SetDistribution"
	share, payout, fee, refund := int32(d.PerformanceShare), int64(d.PayoutAmount), int64(d.FeeCharged), int64(d.RefundAmount)
	n, err := r.s.q(ctx).SetPlacementDistribution(ctx, sqlc.SetPlacementDistributionParams{
		ID: id, PerformanceShare: &share, PayoutAmount: &payout, FeeCharged: &fee, RefundAmount: &refund,
	})
	if err != nil {
		return r.s.fail(ctx, op, err, "placement_id", id)
	}
	if n == 0 {
		return domain.ErrStaleVersion
	}
	return nil
}

func (r *Placements) ListByOrder(ctx context.Context, orderID uuid.UUID) ([]domain.Placement, error) {
	rows, err := r.s.q(ctx).ListPlacementsByOrder(ctx, orderID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.ListByOrder", err)
	}
	return toPlacements(rows), nil
}

func (r *Placements) ListByOrderForUpdate(ctx context.Context, orderID uuid.UUID) ([]domain.Placement, error) {
	rows, err := r.s.q(ctx).ListPlacementsByOrderForUpdate(ctx, orderID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.ListByOrderForUpdate", err)
	}
	return toPlacements(rows), nil
}

func (r *Placements) ListForBlogger(ctx context.Context, orgID uuid.UUID, status *domain.Status, channelID *uuid.UUID,
	after *domain.PageCursor, limit int) ([]domain.Placement, error) {
	const op = "Placement.Placements.ListForBlogger"
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	at, aid := cursorArgs(after)
	rows, err := r.s.q(ctx).ListBloggerPlacements(ctx, sqlc.ListBloggerPlacementsParams{
		OrganizationID: orgID, Status: st, ChannelID: channelID, AfterCreatedAt: at, AfterID: aid, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", len(rows))
	return toPlacements(rows), nil
}

func (r *Placements) BusyIntervals(ctx context.Context, channelID uuid.UUID, from, to time.Time, exclude uuid.UUID) ([]domain.Interval, error) {
	rows, err := r.s.q(ctx).BusyIntervals(ctx, sqlc.BusyIntervalsParams{ChannelID: channelID, ExcludeID: exclude, FromAt: from, ToAt: to})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.BusyIntervals", err)
	}
	out := make([]domain.Interval, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TopInterval(row.StartsAt, int(row.TopHours)))
	}
	return out, nil
}

func (r *Placements) ListByChannel(ctx context.Context, channelID uuid.UUID, statuses []domain.Status) ([]domain.Placement, error) {
	st := make([]string, 0, len(statuses))
	for _, s := range statuses {
		st = append(st, string(s))
	}
	rows, err := r.s.q(ctx).ListPlacementsByChannel(ctx, sqlc.ListPlacementsByChannelParams{ChannelID: channelID, Statuses: st})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.ListByChannel", err)
	}
	return toPlacements(rows), nil
}

func (r *Placements) ScheduledOverlapping(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]uuid.UUID, error) {
	ids, err := r.s.q(ctx).ScheduledOverlapping(ctx, sqlc.ScheduledOverlappingParams{ChannelID: channelID, FromAt: from, ToAt: to})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.ScheduledOverlapping", err)
	}
	return ids, nil
}

func (r *Placements) CountByStatus(ctx context.Context) (map[domain.Status]int, error) {
	rows, err := r.s.q(ctx).CountPlacementsByStatus(ctx)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.CountByStatus", err)
	}
	out := map[domain.Status]int{}
	for _, row := range rows {
		out[domain.Status(row.Status)] = int(row.N)
	}
	return out, nil
}

func (r *Placements) CountByOrders(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID]map[domain.Status]int, error) {
	out := map[uuid.UUID]map[domain.Status]int{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).CountPlacementsByOrders(ctx, orderIDs)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.CountByOrders", err)
	}
	for _, row := range rows {
		if out[row.OrderID] == nil {
			out[row.OrderID] = map[domain.Status]int{}
		}
		out[row.OrderID][domain.Status(row.Status)] = int(row.N)
	}
	return out, nil
}

func (r *Placements) OverdueScheduled(ctx context.Context, before time.Time) ([]uuid.UUID, error) {
	ids, err := r.s.q(ctx).OverdueScheduled(ctx, &before)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.OverdueScheduled", err)
	}
	return ids, nil
}

func (r *Placements) ScheduledWithoutPublication(ctx context.Context, before time.Time) ([]domain.ScheduledRef, error) {
	rows, err := r.s.q(ctx).ScheduledWithoutPublication(ctx, &before)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Placements.ScheduledWithoutPublication", err)
	}
	out := make([]domain.ScheduledRef, 0, len(rows))
	for _, row := range rows {
		ref := domain.ScheduledRef{ID: row.ID}
		if row.PublishAt != nil {
			ref.PublishAt = *row.PublishAt
		}
		out = append(out, ref)
	}
	return out, nil
}
