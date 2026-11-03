package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
)

// Campaigns — кампании организации.
type Campaigns struct{ s *Store }

func pgDate(d *domain.Date) pgtype.Date {
	if d == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *d, Valid: true}
}

func fromPgDate(d pgtype.Date) *domain.Date {
	if !d.Valid {
		return nil
	}
	v := domain.NewDate(d.Time.Year(), d.Time.Month(), d.Time.Day())
	return &v
}

func pgTime(t *domain.TimeOfDay) pgtype.Time {
	if t == nil {
		return pgtype.Time{}
	}
	return pgtype.Time{Microseconds: int64(*t) * int64(time.Minute/time.Microsecond), Valid: true}
}

func fromPgTime(t pgtype.Time) *domain.TimeOfDay {
	if !t.Valid {
		return nil
	}
	v := domain.TimeOfDay(t.Microseconds / int64(time.Minute/time.Microsecond))
	return &v
}

func toCampaign(r sqlc.Campaign) domain.Campaign {
	return domain.Campaign{
		ID: r.ID, OrganizationID: r.OrganizationID, Name: r.Name, Status: domain.CampaignStatus(r.Status),
		DateFrom: fromPgDate(r.DateFrom), DateTo: fromPgDate(r.DateTo),
		TimeFrom: fromPgTime(r.TimeFrom), TimeTo: fromPgTime(r.TimeTo),
		ExtraRequirements: r.ExtraRequirements, OrderID: r.OrderID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

func (r *Campaigns) Insert(ctx context.Context, c domain.Campaign) error {
	const op = "Post.Campaigns.Insert"
	if err := r.s.q(ctx).InsertCampaign(ctx, sqlc.InsertCampaignParams{
		ID: c.ID, OrganizationID: c.OrganizationID, Name: c.Name, Status: string(c.Status),
		DateFrom: pgDate(c.DateFrom), DateTo: pgDate(c.DateTo), TimeFrom: pgTime(c.TimeFrom), TimeTo: pgTime(c.TimeTo),
		ExtraRequirements: c.ExtraRequirements, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "campaign_id", c.ID)
	return nil
}

func (r *Campaigns) Get(ctx context.Context, orgID, id uuid.UUID) (domain.Campaign, error) {
	row, err := r.s.q(ctx).GetCampaign(ctx, sqlc.GetCampaignParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Campaign{}, r.s.fail(ctx, "Post.Campaigns.Get", err)
	}
	return toCampaign(row), nil
}

func (r *Campaigns) GetForUpdate(ctx context.Context, orgID, id uuid.UUID) (domain.Campaign, error) {
	row, err := r.s.q(ctx).GetCampaignForUpdate(ctx, sqlc.GetCampaignForUpdateParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Campaign{}, r.s.fail(ctx, "Post.Campaigns.GetForUpdate", err)
	}
	return toCampaign(row), nil
}

func (r *Campaigns) List(ctx context.Context, orgID uuid.UUID, status *domain.CampaignStatus, after *domain.PageCursor, limit int) ([]domain.Campaign, error) {
	const op = "Post.Campaigns.List"
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	at, aid := cursorArgs(after)
	rows, err := r.s.q(ctx).ListCampaigns(ctx, sqlc.ListCampaignsParams{
		OrganizationID: orgID, Status: st, AfterCreatedAt: at, AfterID: aid, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", len(rows))
	out := make([]domain.Campaign, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCampaign(row))
	}
	return out, nil
}

func (r *Campaigns) Update(ctx context.Context, c domain.Campaign) error {
	const op = "Post.Campaigns.Update"
	if err := r.s.q(ctx).UpdateCampaign(ctx, sqlc.UpdateCampaignParams{
		ID: c.ID, OrganizationID: c.OrganizationID, Name: c.Name, Status: string(c.Status),
		DateFrom: pgDate(c.DateFrom), DateTo: pgDate(c.DateTo), TimeFrom: pgTime(c.TimeFrom), TimeTo: pgTime(c.TimeTo),
		ExtraRequirements: c.ExtraRequirements, OrderID: c.OrderID, UpdatedAt: c.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "campaign_id", c.ID)
	return nil
}
