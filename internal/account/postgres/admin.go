package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/account/postgres/sqlc"
)

// Организации для администратора (этап «API администратора», R-13).

func (r *Organizations) AdminList(ctx context.Context, f domain.AdminOrganizationFilter) ([]domain.AdminOrganizationItem, int, error) {
	const op = "Account.Organizations.AdminList"
	r.s.debug(ctx, op, "cabinet", f.Cabinet, "offset", f.Offset, "limit", f.Limit)
	q := r.s.q(ctx)
	rows, err := q.AdminListOrganizations(ctx, sqlc.AdminListOrganizationsParams{
		Query: f.Query, ExactInn: f.ExactINN, Cabinet: string(f.Cabinet), Off: int32(f.Offset), Lim: int32(f.Limit),
	})
	if err != nil {
		return nil, 0, r.s.fail(ctx, op, err)
	}
	total, err := q.AdminCountOrganizations(ctx, sqlc.AdminCountOrganizationsParams{Query: f.Query, ExactInn: f.ExactINN, Cabinet: string(f.Cabinet)})
	if err != nil {
		return nil, 0, r.s.fail(ctx, op, err)
	}
	out := make([]domain.AdminOrganizationItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.AdminOrganizationItem{
			ID: row.ID, Name: row.Name, INN: row.Inn, LegalType: domain.LegalType(row.LegalType),
			Status: domain.Status(row.Status), Advertiser: row.IsAdvertiser, Blogger: row.IsBlogger, CreatedAt: row.CreatedAt,
		})
	}
	return out, int(total), nil
}

func (r *Organizations) AdminGet(ctx context.Context, id uuid.UUID) (*domain.Organization, error) {
	const op = "Account.Organizations.AdminGet"
	r.s.debug(ctx, op, "organization_id", id)
	row, err := r.s.q(ctx).AdminGetOrganization(ctx, id)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return organizationFromRow(row.Organization, row.IsAdvertiser, row.IsBlogger, row.BloggerFullName), nil
}

func (r *Organizations) Members(ctx context.Context, organizationID uuid.UUID) ([]domain.User, error) {
	const op = "Account.Organizations.Members"
	r.s.debug(ctx, op, "organization_id", organizationID)
	rows, err := r.s.q(ctx).ListOrganizationMembers(ctx, organizationID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, *userFromRow(row))
	}
	return out, nil
}
