package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
)

// Contracts — репозиторий ord_contract.
type Contracts struct{ *Store }

func contractFromRow(r sqlc.OrdContract) domain.Contract {
	return domain.Contract{
		ID: r.ID, Kind: domain.ContractKind(r.Kind), ClientOrganizationID: r.ClientOrganizationID,
		ContractorOrganizationID: r.ContractorOrganizationID, Number: str(r.Number), SignedDate: dateVal(r.SignedDate),
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// ByParties — договор вида по сторонам без блокировки.
func (r *Contracts) ByParties(ctx context.Context, kind domain.ContractKind, client, contractor uuid.UUID) (domain.Contract, error) {
	row, err := r.q(ctx).GetOrdContractByParties(ctx, sqlc.GetOrdContractByPartiesParams{
		Kind: string(kind), ClientOrganizationID: client, ContractorOrganizationID: contractor,
	})
	if err != nil {
		return domain.Contract{}, r.fail(ctx, "OrdContracts.ByParties", err)
	}
	return contractFromRow(row), nil
}

// GetForUpdate блокирует запись.
func (r *Contracts) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Contract, error) {
	row, err := r.q(ctx).GetOrdContractForUpdate(ctx, id)
	if err != nil {
		return domain.Contract{}, r.fail(ctx, "OrdContracts.GetForUpdate", err)
	}
	return contractFromRow(row), nil
}

// Insert вставляет запись; при гонке возвращает существующую.
func (r *Contracts) Insert(ctx context.Context, ct domain.Contract) (domain.Contract, bool, error) {
	c := toCols(ct.Sync)
	n, err := r.q(ctx).InsertOrdContract(ctx, sqlc.InsertOrdContractParams{
		ID: ct.ID, Kind: string(ct.Kind), ClientOrganizationID: ct.ClientOrganizationID,
		ContractorOrganizationID: ct.ContractorOrganizationID, Number: text(ct.Number), SignedDate: pgDate(ct.SignedDate),
		OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error,
		SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus,
		SentAt: c.SentAt, CreatedAt: ct.CreatedAt, UpdatedAt: ct.UpdatedAt,
	})
	if err != nil {
		return domain.Contract{}, false, r.fail(ctx, "OrdContracts.Insert", err)
	}
	if n == 0 {
		existing, err := r.ByParties(ctx, ct.Kind, ct.ClientOrganizationID, ct.ContractorOrganizationID)
		return existing, false, err
	}
	r.saved(ctx, "OrdContracts.Insert", domain.TypeContract, ct.ID, ct.Sync.Status)
	return ct, true, nil
}

// Save сохраняет запись.
func (r *Contracts) Save(ctx context.Context, ct domain.Contract) error {
	c := toCols(ct.Sync)
	if err := r.q(ctx).SaveOrdContract(ctx, sqlc.SaveOrdContractParams{
		ID: ct.ID, Number: text(ct.Number), SignedDate: pgDate(ct.SignedDate), OrdExternalID: c.ExternalID,
		SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts,
		SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, UpdatedAt: ct.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdContracts.Save", err)
	}
	r.saved(ctx, "OrdContracts.Save", domain.TypeContract, ct.ID, ct.Sync.Status)
	return nil
}
