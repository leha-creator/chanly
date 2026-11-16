package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type PayoutActDetails struct {
	BloggerOrgID            uuid.UUID
	OrderNumber             int64
	PublicationURL          string
	TopHours, DurationHours int
	PublishedAt, RemovedAt  *time.Time
}

func (r *Placements) ForPayoutAct(ctx context.Context, id uuid.UUID) (PayoutActDetails, error) {
	row, err := r.s.q(ctx).GetPlacementForPayoutAct(ctx, id)
	if err != nil {
		return PayoutActDetails{}, r.s.fail(ctx, "Placement.ForPayoutAct", err)
	}
	var link string
	if row.Url != nil {
		link = *row.Url
	}
	return PayoutActDetails{BloggerOrgID: row.BloggerOrganizationID, OrderNumber: row.OrderNumber,
		PublicationURL: link, TopHours: int(row.TopHours), DurationHours: int(row.DurationHours),
		PublishedAt: &row.PublishedAt, RemovedAt: row.RemovedAt}, nil
}
