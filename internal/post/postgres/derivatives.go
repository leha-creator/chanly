package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
)

// Derivatives — производные изображений.
type Derivatives struct{ s *Store }

func toDerivative(r sqlc.MediaDerivative) domain.Derivative {
	return domain.Derivative{
		ID: r.ID, MediaFileID: r.MediaFileID, Kind: domain.DerivativeKind(r.Kind), ParamsHash: r.ParamsHash,
		Status: domain.DerivativeStatus(r.Status), StorageKey: str(r.StorageKey), Mime: str(r.Mime),
		Width: i32(r.Width), Height: i32(r.Height), SizeBytes: i64(r.SizeBytes),
		PublicStorageKey: str(r.PublicStorageKey), PublicURL: str(r.PublicUrl), PublishedAt: r.PublishedAt,
		CreatedAt: r.CreatedAt, PurgedAt: r.PurgedAt,
	}
}

func (r *Derivatives) Ensure(ctx context.Context, fileID uuid.UUID, kind domain.DerivativeKind, hash string, now time.Time) (domain.Derivative, bool, error) {
	const op = "Post.Derivatives.Ensure"
	row, err := r.s.q(ctx).EnsureDerivative(ctx, sqlc.EnsureDerivativeParams{
		ID: ids.New(), MediaFileID: fileID, Kind: string(kind), ParamsHash: hash, Now: now,
	})
	if err != nil {
		return domain.Derivative{}, false, r.s.fail(ctx, op, err)
	}
	d := toDerivative(row)
	r.s.debug(ctx, op, "media_file_id", fileID, "kind", kind, "derivative_id", d.ID, "status", d.Status)
	return d, d.Status == domain.DerivativePending, nil
}

func (r *Derivatives) Get(ctx context.Context, id uuid.UUID) (domain.Derivative, error) {
	row, err := r.s.q(ctx).GetDerivative(ctx, id)
	if err != nil {
		return domain.Derivative{}, r.s.fail(ctx, "Post.Derivatives.Get", err)
	}
	return toDerivative(row), nil
}

func (r *Derivatives) Find(ctx context.Context, fileID uuid.UUID, kind domain.DerivativeKind, hash string) (domain.Derivative, bool, error) {
	row, err := r.s.q(ctx).FindDerivative(ctx, sqlc.FindDerivativeParams{MediaFileID: fileID, Kind: string(kind), ParamsHash: hash})
	if err != nil {
		if err = r.s.fail(ctx, "Post.Derivatives.Find", err); err == domain.ErrNotFound {
			return domain.Derivative{}, false, nil
		}
		return domain.Derivative{}, false, err
	}
	return toDerivative(row), true, nil
}

func (r *Derivatives) ListByFiles(ctx context.Context, fileIDs []uuid.UUID, kind domain.DerivativeKind) ([]domain.Derivative, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	rows, err := r.s.q(ctx).ListDerivativesByFiles(ctx, sqlc.ListDerivativesByFilesParams{FileIds: fileIDs, Kind: string(kind)})
	if err != nil {
		return nil, r.s.fail(ctx, "Post.Derivatives.ListByFiles", err)
	}
	out := make([]domain.Derivative, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDerivative(row))
	}
	return out, nil
}

func (r *Derivatives) MarkReady(ctx context.Context, d domain.Derivative) (bool, error) {
	const op = "Post.Derivatives.MarkReady"
	w, h, size := int32(d.Width), int32(d.Height), d.SizeBytes
	n, err := r.s.q(ctx).MarkDerivativeReady(ctx, sqlc.MarkDerivativeReadyParams{
		ID: d.ID, StorageKey: text(d.StorageKey), Mime: text(d.Mime), Width: &w, Height: &h, SizeBytes: &size,
	})
	if err != nil {
		return false, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "derivative_id", d.ID, "updated", n == 1)
	return n == 1, nil
}

func (r *Derivatives) MarkPublished(ctx context.Context, id uuid.UUID, publicKey, publicURL string, now time.Time) (bool, error) {
	const op = "Post.Derivatives.MarkPublished"
	n, err := r.s.q(ctx).MarkDerivativePublished(ctx, sqlc.MarkDerivativePublishedParams{
		ID: id, PublicStorageKey: &publicKey, PublicUrl: &publicURL, Now: &now,
	})
	if err != nil {
		return false, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "derivative_id", id, "updated", n == 1)
	return n == 1, nil
}

func (r *Derivatives) ForPurge(ctx context.Context, before time.Time, limit int) ([]domain.Derivative, error) {
	rows, err := r.s.q(ctx).ListDerivativesForPurge(ctx, sqlc.ListDerivativesForPurgeParams{Before: before, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, "Post.Derivatives.ForPurge", err)
	}
	out := make([]domain.Derivative, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDerivative(row))
	}
	return out, nil
}

func (r *Derivatives) MarkPurged(ctx context.Context, id uuid.UUID, now time.Time) error {
	if err := r.s.q(ctx).MarkDerivativePurged(ctx, sqlc.MarkDerivativePurgedParams{ID: id, Now: &now}); err != nil {
		return r.s.fail(ctx, "Post.Derivatives.MarkPurged", err)
	}
	return nil
}
