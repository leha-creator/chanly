package postgres

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
)

// OrdReport — выборки ежемесячной отчётности ОРД (R-19, R-20).
type OrdReport struct{ s *Store }

// OrdReport возвращает выборки отчётности.
func (s *Store) OrdReport() *OrdReport { return &OrdReport{s} }

// Placements — размещения с публикацией, пересекающей [from, to).
func (r *OrdReport) Placements(ctx context.Context, from, to time.Time) ([]domain.OrdReportRow, error) {
	const op = "Placement.Report.Placements"
	rows, err := r.s.q(ctx).ReportPlacements(ctx, sqlc.ReportPlacementsParams{PeriodFrom: from, PeriodTo: to})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.OrdReportRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.OrdReportRow{
			FeeAmount: int64(row.FeeAmount), FeeCharged: row.FeeCharged,
			PlacementID: row.PlacementID, PublicationID: row.PublicationID, ChannelID: row.ChannelID,
			PostVersionID: row.PostVersionID, AdvertiserOrganizationID: row.AdvertiserOrganizationID,
			BloggerOrganizationID: row.BloggerOrganizationID, PublishedAt: row.PublishedAt, RemovedAt: row.RemovedAt,
			Views: widen(row.Views), FinalViews: widen(row.FinalViews), TotalAmount: int64(row.TotalAmount),
			RefundAmount: row.RefundAmount, BloggerAmount: int64(row.BloggerAmount), PayoutAmount: row.PayoutAmount,
		})
	}
	return out, nil
}

// LiveViews — публикации в канале на момент at и их последние показы.
func (r *OrdReport) LiveViews(ctx context.Context, at time.Time) ([]domain.OrdLiveViews, error) {
	const op = "Placement.Report.LiveViews"
	rows, err := r.s.q(ctx).LivePublicationViews(ctx, at)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.OrdLiveViews, 0, len(rows))
	for _, row := range rows {
		if row.Views == nil {
			continue
		}
		v := domain.OrdLiveViews{PublicationID: row.ID, CheckedAt: row.ViewsCheckedAt}
		if row.Views != nil {
			v.Views = int64(*row.Views)
		}
		out = append(out, v)
	}
	return out, nil
}

func widen(v *int32) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}
