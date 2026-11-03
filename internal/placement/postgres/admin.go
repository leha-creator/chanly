package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Admin — выборки разделов администратора.
type Admin struct{ s *Store }

// Admin возвращает выборки администратора.
func (s *Store) Admin() *Admin { return &Admin{s} }

// DisputeMessages возвращает репозиторий переписки по спору.
func (s *Store) DisputeMessages() *DisputeMessages { return &DisputeMessages{s} }

// DisputeMessages — переписка по спору.
type DisputeMessages struct{ s *Store }

var (
	_ domain.AdminReads      = (*Admin)(nil)
	_ domain.DisputeMessages = (*DisputeMessages)(nil)
)

func strPtr[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Admin) Orders(ctx context.Context, f domain.AdminOrderFilter) ([]domain.AdminOrderRow, int, error) {
	rows, err := r.s.q(ctx).ListAdminOrders(ctx, sqlc.ListAdminOrdersParams{
		Status: strPtr(f.Status), Query: nonEmpty(f.Query), ByDeadline: f.ByDeadline, Off: int32(f.Offset), Lim: int32(f.Limit),
	})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "Placement.Admin.Orders", err)
	}
	out := make([]domain.AdminOrderRow, 0, len(rows))
	total := 0
	for _, row := range rows {
		total = int(row.Total)
		out = append(out, domain.AdminOrderRow{
			ID: row.ID, Number: row.Number, OrganizationID: row.OrganizationID, Status: domain.OrderStatus(row.Status),
			TotalAmount: money.Kopecks(row.TotalAmount), CreatedAt: row.CreatedAt, CampaignName: row.CampaignName,
			PublishFrom: row.PublishFrom, PlacementsCount: int(row.PlacementsCount), WaitingSince: row.WaitingSince,
		})
	}
	return out, total, nil
}

func (r *Admin) Placements(ctx context.Context, f domain.AdminPlacementFilter) ([]domain.AdminPlacementRow, int, error) {
	rows, err := r.s.q(ctx).ListAdminPlacements(ctx, sqlc.ListAdminPlacementsParams{
		Now: f.Now, Status: strPtr(f.Status), ChannelID: f.ChannelID, OrderID: f.OrderID, CreatedFrom: f.CreatedFrom,
		CreatedTo: f.CreatedTo, OnlyRemovalOverdue: f.RemovalOverdue, Off: int32(f.Offset), Lim: int32(f.Limit),
	})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "Placement.Admin.Placements", err)
	}
	out := make([]domain.AdminPlacementRow, 0, len(rows))
	total := 0
	for _, row := range rows {
		total = int(row.Total)
		out = append(out, domain.AdminPlacementRow{Placement: toPlacement(row.Placement), OrderNumber: row.OrderNumber,
			AdvertiserID: row.AdvertiserID, RemovalOverdue: row.RemovalOverdue})
	}
	return out, total, nil
}

func (r *Admin) Disputes(ctx context.Context, f domain.AdminDisputeListFilter) ([]domain.AdminDisputeListRow, int, error) {
	rows, err := r.s.q(ctx).ListAdminDisputes(ctx, sqlc.ListAdminDisputesParams{
		State: string(f.State), OverdueAt: f.OverdueAt, Off: int32(f.Offset), Lim: int32(f.Limit),
	})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "Placement.Admin.Disputes", err)
	}
	out := make([]domain.AdminDisputeListRow, 0, len(rows))
	total := 0
	for _, row := range rows {
		total = int(row.Total)
		out = append(out, domain.AdminDisputeListRow{
			Dispute: toDispute(row.Dispute), PlacementStatus: domain.Status(row.PlacementStatus),
			TotalAmount: money.Kopecks(row.TotalAmount), ChannelID: row.ChannelID, BloggerOrgID: row.BloggerOrganizationID,
			OrderNumber: row.OrderNumber, AdvertiserID: row.AdvertiserID,
		})
	}
	return out, total, nil
}

func (r *Admin) DisputeByID(ctx context.Context, id uuid.UUID) (domain.Dispute, error) {
	row, err := r.s.q(ctx).GetDisputeByID(ctx, id)
	if err != nil {
		return domain.Dispute{}, r.s.fail(ctx, "Placement.Admin.DisputeByID", err)
	}
	return toDispute(row), nil
}

func (r *Admin) Counts(ctx context.Context, now time.Time) (domain.AdminCounts, error) {
	row, err := r.s.q(ctx).CountAdminQueues(ctx, now)
	if err != nil {
		return domain.AdminCounts{}, r.s.fail(ctx, "Placement.Admin.Counts", err)
	}
	return domain.AdminCounts{OrdersModeration: int(row.OrdersModeration), DisputesOpen: int(row.DisputesOpen),
		DisputesOverdue: int(row.DisputesOverdue), RemovalsOverdue: int(row.RemovalsOverdue)}, nil
}

func (r *DisputeMessages) Insert(ctx context.Context, m domain.DisputeMessage) error {
	const op = "Placement.DisputeMessages.Insert"
	var recipient *string
	if m.Recipient != "" {
		v := string(m.Recipient)
		recipient = &v
	}
	err := r.s.q(ctx).InsertDisputeMessage(ctx, sqlc.InsertDisputeMessageParams{
		ID: m.ID, DisputeID: m.DisputeID, AuthorType: string(m.Author), AuthorUserID: m.AuthorUserID, Recipient: recipient,
		Text: m.Text, CreatedAt: m.CreatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, op, err, "dispute_id", m.DisputeID)
	}
	r.s.debug(ctx, op, "dispute_id", m.DisputeID, "author_type", m.Author)
	return nil
}

func (r *DisputeMessages) List(ctx context.Context, disputeID uuid.UUID) ([]domain.DisputeMessage, error) {
	rows, err := r.s.q(ctx).ListDisputeMessages(ctx, disputeID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.DisputeMessages.List", err)
	}
	out := make([]domain.DisputeMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.DisputeMessage{ID: row.ID, DisputeID: row.DisputeID, Author: domain.MessageAuthor(row.AuthorType),
			AuthorUserID: row.AuthorUserID, Recipient: domain.MessageRecipient(str(row.Recipient)), Text: row.Text,
			CreatedAt: row.CreatedAt})
	}
	return out, nil
}
