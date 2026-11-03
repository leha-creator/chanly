package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
)

// Platforms — репозиторий ord_platform.
type Platforms struct{ *Store }

func platformFromRow(r sqlc.OrdPlatform) domain.Platform {
	return domain.Platform{
		ID: r.ID, ChannelID: r.ChannelID, OrganizationID: r.OrganizationID, Name: r.Name, URL: r.Url,
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// ByChannel — площадка канала без блокировки.
func (r *Platforms) ByChannel(ctx context.Context, channelID uuid.UUID) (domain.Platform, error) {
	row, err := r.q(ctx).GetOrdPlatformByChannel(ctx, channelID)
	if err != nil {
		return domain.Platform{}, r.fail(ctx, "OrdPlatforms.ByChannel", err)
	}
	return platformFromRow(row), nil
}

// GetForUpdate блокирует запись.
func (r *Platforms) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Platform, error) {
	row, err := r.q(ctx).GetOrdPlatformForUpdate(ctx, id)
	if err != nil {
		return domain.Platform{}, r.fail(ctx, "OrdPlatforms.GetForUpdate", err)
	}
	return platformFromRow(row), nil
}

// Insert вставляет запись; при гонке возвращает существующую.
func (r *Platforms) Insert(ctx context.Context, p domain.Platform) (domain.Platform, bool, error) {
	c := toCols(p.Sync)
	n, err := r.q(ctx).InsertOrdPlatform(ctx, sqlc.InsertOrdPlatformParams{
		ID: p.ID, ChannelID: p.ChannelID, OrganizationID: p.OrganizationID, Name: p.Name, Url: p.URL,
		OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error,
		SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus,
		SentAt: c.SentAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	})
	if err != nil {
		return domain.Platform{}, false, r.fail(ctx, "OrdPlatforms.Insert", err)
	}
	if n == 0 {
		existing, err := r.ByChannel(ctx, p.ChannelID)
		return existing, false, err
	}
	r.saved(ctx, "OrdPlatforms.Insert", domain.TypePlatform, p.ID, p.Sync.Status)
	return p, true, nil
}

// Save сохраняет запись.
func (r *Platforms) Save(ctx context.Context, p domain.Platform) error {
	c := toCols(p.Sync)
	if err := r.q(ctx).SaveOrdPlatform(ctx, sqlc.SaveOrdPlatformParams{
		ID: p.ID, OrganizationID: p.OrganizationID, Name: p.Name, Url: p.URL, OrdExternalID: c.ExternalID,
		SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts,
		SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, UpdatedAt: p.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdPlatforms.Save", err)
	}
	r.saved(ctx, "OrdPlatforms.Save", domain.TypePlatform, p.ID, p.Sync.Status)
	return nil
}
