package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
)

// MediaFiles — оригиналы медиатеки.
type MediaFiles struct{ s *Store }

func toMediaFile(r sqlc.MediaFile) domain.MediaFile {
	return domain.MediaFile{
		ID: r.ID, OrganizationID: r.OrganizationID, StorageKey: r.StorageKey, Mime: r.Mime, SizeBytes: r.SizeBytes,
		Width: int(r.Width), Height: int(r.Height), SHA256: r.Sha256, OriginalName: r.OriginalName,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, DeletedAt: r.DeletedAt, PurgedAt: r.PurgedAt,
	}
}

func toMediaFiles(rows []sqlc.MediaFile) []domain.MediaFile {
	out := make([]domain.MediaFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, toMediaFile(r))
	}
	return out
}

func (r *MediaFiles) Insert(ctx context.Context, f domain.MediaFile) error {
	const op = "Post.MediaFiles.Insert"
	if err := r.s.q(ctx).InsertMediaFile(ctx, sqlc.InsertMediaFileParams{
		ID: f.ID, OrganizationID: f.OrganizationID, StorageKey: f.StorageKey, Mime: f.Mime, SizeBytes: f.SizeBytes,
		Width: int32(f.Width), Height: int32(f.Height), Sha256: f.SHA256, OriginalName: f.OriginalName,
		CreatedBy: f.CreatedBy, CreatedAt: f.CreatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "media_file_id", f.ID)
	return nil
}

func (r *MediaFiles) Get(ctx context.Context, orgID, id uuid.UUID) (domain.MediaFile, error) {
	row, err := r.s.q(ctx).GetMediaFile(ctx, sqlc.GetMediaFileParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.MediaFile{}, r.s.fail(ctx, "Post.MediaFiles.Get", err)
	}
	return toMediaFile(row), nil
}

func (r *MediaFiles) GetAny(ctx context.Context, id uuid.UUID) (domain.MediaFile, error) {
	row, err := r.s.q(ctx).GetMediaFileAny(ctx, id)
	if err != nil {
		return domain.MediaFile{}, r.s.fail(ctx, "Post.MediaFiles.GetAny", err)
	}
	return toMediaFile(row), nil
}

func (r *MediaFiles) FindBySHA256(ctx context.Context, orgID uuid.UUID, sha string) (domain.MediaFile, bool, error) {
	row, err := r.s.q(ctx).FindMediaFileBySHA256(ctx, sqlc.FindMediaFileBySHA256Params{OrganizationID: orgID, Sha256: sha})
	if err != nil {
		if err = r.s.fail(ctx, "Post.MediaFiles.FindBySHA256", err); err == domain.ErrNotFound {
			return domain.MediaFile{}, false, nil
		}
		return domain.MediaFile{}, false, err
	}
	return toMediaFile(row), true, nil
}

func (r *MediaFiles) List(ctx context.Context, orgID uuid.UUID, after *domain.PageCursor, limit int) ([]domain.MediaFile, error) {
	const op = "Post.MediaFiles.List"
	at, aid := cursorArgs(after)
	rows, err := r.s.q(ctx).ListMediaFiles(ctx, sqlc.ListMediaFilesParams{
		OrganizationID: orgID, AfterCreatedAt: at, AfterID: aid, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.log.DebugContext(ctx, "media files listed", "op", op, "count", len(rows))
	return toMediaFiles(rows), nil
}

func (r *MediaFiles) LiveSubset(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]domain.MediaFile, error) {
	rows, err := r.s.q(ctx).ListLiveMediaFiles(ctx, sqlc.ListLiveMediaFilesParams{OrganizationID: orgID, Ids: ids})
	if err != nil {
		return nil, r.s.fail(ctx, "Post.MediaFiles.LiveSubset", err)
	}
	return toMediaFiles(rows), nil
}

func (r *MediaFiles) SoftDelete(ctx context.Context, orgID, id uuid.UUID, now time.Time) error {
	const op = "Post.MediaFiles.SoftDelete"
	n, err := r.s.q(ctx).SoftDeleteMediaFile(ctx, sqlc.SoftDeleteMediaFileParams{ID: id, OrganizationID: orgID, Now: &now})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	r.s.debug(ctx, op, "media_file_id", id)
	return nil
}

func (r *MediaFiles) ForPurge(ctx context.Context, before time.Time, limit int) ([]domain.MediaFile, error) {
	rows, err := r.s.q(ctx).ListMediaFilesForPurge(ctx, sqlc.ListMediaFilesForPurgeParams{Before: before, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, "Post.MediaFiles.ForPurge", err)
	}
	return toMediaFiles(rows), nil
}

func (r *MediaFiles) MarkPurged(ctx context.Context, id uuid.UUID, now time.Time) error {
	if err := r.s.q(ctx).MarkMediaFilePurged(ctx, sqlc.MarkMediaFilePurgedParams{ID: id, Now: &now}); err != nil {
		return r.s.fail(ctx, "Post.MediaFiles.MarkPurged", err)
	}
	return nil
}
