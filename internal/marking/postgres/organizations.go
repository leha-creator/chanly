package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
)

// Organizations — репозиторий ord_organization.
type Organizations struct{ *Store }

func organizationFromRow(r sqlc.OrdOrganization) domain.Organization {
	return domain.Organization{
		ID: r.ID, OrganizationID: r.OrganizationID, IsRr: r.IsRr,
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// ByOrganization — контрагент организации без блокировки.
func (r *Organizations) ByOrganization(ctx context.Context, orgID uuid.UUID) (domain.Organization, error) {
	row, err := r.q(ctx).GetOrdOrganizationByOrganization(ctx, orgID)
	if err != nil {
		return domain.Organization{}, r.fail(ctx, "OrdOrganizations.ByOrganization", err)
	}
	return organizationFromRow(row), nil
}

// GetForUpdate блокирует запись.
func (r *Organizations) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Organization, error) {
	row, err := r.q(ctx).GetOrdOrganizationForUpdate(ctx, id)
	if err != nil {
		return domain.Organization{}, r.fail(ctx, "OrdOrganizations.GetForUpdate", err)
	}
	return organizationFromRow(row), nil
}

// Insert вставляет запись; при гонке возвращает существующую.
func (r *Organizations) Insert(ctx context.Context, o domain.Organization) (domain.Organization, bool, error) {
	c := toCols(o.Sync)
	n, err := r.q(ctx).InsertOrdOrganization(ctx, sqlc.InsertOrdOrganizationParams{
		ID: o.ID, OrganizationID: o.OrganizationID, IsRr: o.IsRr, OrdExternalID: c.ExternalID, SyncStatus: c.Status,
		SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt,
		PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	})
	if err != nil {
		return domain.Organization{}, false, r.fail(ctx, "OrdOrganizations.Insert", err)
	}
	if n == 0 {
		existing, err := r.ByOrganization(ctx, o.OrganizationID)
		return existing, false, err
	}
	r.saved(ctx, "OrdOrganizations.Insert", domain.TypeOrganization, o.ID, o.Sync.Status)
	return o, true, nil
}

// Save сохраняет запись.
func (r *Organizations) Save(ctx context.Context, o domain.Organization) error {
	c := toCols(o.Sync)
	if err := r.q(ctx).SaveOrdOrganization(ctx, sqlc.SaveOrdOrganizationParams{
		ID: o.ID, IsRr: o.IsRr, OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID,
		SyncError: c.Error, SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash,
		OrdStatus: c.OrdStatus, SentAt: c.SentAt, UpdatedAt: o.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdOrganizations.Save", err)
	}
	r.saved(ctx, "OrdOrganizations.Save", domain.TypeOrganization, o.ID, o.Sync.Status)
	return nil
}
