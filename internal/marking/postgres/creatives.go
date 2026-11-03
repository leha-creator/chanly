package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Creatives — репозиторий creative.
type Creatives struct{ *Store }

func creativeFromRow(r sqlc.Creative) domain.Creative {
	return domain.Creative{
		ID: r.ID, PostVersionID: r.PostVersionID, ContractID: uuidVal(r.OrdContractID), Erid: str(r.Erid),
		EridReceivedAt: ts(r.EridReceivedAt), ErirStatus: domain.ErirStatus(r.ErirStatus), ErirError: str(r.ErirError),
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// ByVersion — креатив версии без блокировки.
func (r *Creatives) ByVersion(ctx context.Context, versionID uuid.UUID) (domain.Creative, error) {
	row, err := r.q(ctx).GetCreativeByVersion(ctx, versionID)
	if err != nil {
		return domain.Creative{}, r.fail(ctx, "Creatives.ByVersion", err)
	}
	return creativeFromRow(row), nil
}

// GetForUpdate блокирует запись.
func (r *Creatives) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Creative, error) {
	row, err := r.q(ctx).GetCreativeForUpdate(ctx, id)
	if err != nil {
		return domain.Creative{}, r.fail(ctx, "Creatives.GetForUpdate", err)
	}
	return creativeFromRow(row), nil
}

// Insert вставляет запись; при гонке возвращает существующую.
func (r *Creatives) Insert(ctx context.Context, cr domain.Creative) (domain.Creative, bool, error) {
	c := toCols(cr.Sync)
	n, err := r.q(ctx).InsertCreative(ctx, sqlc.InsertCreativeParams{
		ID: cr.ID, PostVersionID: cr.PostVersionID, OrdContractID: uuidPtr(cr.ContractID), Erid: text(cr.Erid),
		EridReceivedAt: tsPtr(cr.EridReceivedAt), ErirStatus: string(cr.ErirStatus), ErirError: text(cr.ErirError),
		OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error,
		SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus,
		SentAt: c.SentAt, CreatedAt: cr.CreatedAt, UpdatedAt: cr.UpdatedAt,
	})
	if err != nil {
		return domain.Creative{}, false, r.fail(ctx, "Creatives.Insert", err)
	}
	if n == 0 {
		existing, err := r.ByVersion(ctx, cr.PostVersionID)
		return existing, false, err
	}
	r.saved(ctx, "Creatives.Insert", domain.TypeCreative, cr.ID, cr.Sync.Status)
	return cr, true, nil
}

// Save сохраняет запись.
func (r *Creatives) Save(ctx context.Context, cr domain.Creative) error {
	c := toCols(cr.Sync)
	if err := r.q(ctx).SaveCreative(ctx, sqlc.SaveCreativeParams{
		ID: cr.ID, OrdContractID: uuidPtr(cr.ContractID), Erid: text(cr.Erid), EridReceivedAt: tsPtr(cr.EridReceivedAt),
		ErirStatus: string(cr.ErirStatus), ErirError: text(cr.ErirError), OrdExternalID: c.ExternalID,
		SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts,
		SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, UpdatedAt: cr.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "Creatives.Save", err)
	}
	r.saved(ctx, "Creatives.Save", domain.TypeCreative, cr.ID, cr.Sync.Status)
	return nil
}

// Delete удаляет креатив без отправки (R-11); отправленный не удаляется — ErrNotFound.
func (r *Creatives) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := r.q(ctx).DeleteUnsentCreative(ctx, id)
	if err != nil {
		return r.fail(ctx, "Creatives.Delete", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	r.log.DebugContext(ctx, "unsent creative deleted", logx.KeyOp, "Creatives.Delete", "creative_id", id)
	return nil
}
