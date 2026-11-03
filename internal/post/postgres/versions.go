package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
)

// Versions — версии поста.
type Versions struct{ s *Store }

// immutable переводит отказ триггера post_version_ready_immutable в недопустимый переход (R-18).
func (r *Versions) immutable(ctx context.Context, op string, err error) error {
	if db.CheckViolation(err, "post_version_ready_immutable") {
		r.s.log.WarnContext(ctx, "post version is immutable", "op", op)
		return apperr.InvalidTransition("update", string(domain.VersionReady)).WithCause(err)
	}
	return r.s.fail(ctx, op, err)
}

func (r *Versions) NextNumber(ctx context.Context, postID uuid.UUID) (int, error) {
	n, err := r.s.q(ctx).NextVersionNumber(ctx, postID)
	if err != nil {
		return 0, r.s.fail(ctx, "Post.Versions.NextNumber", err)
	}
	return int(n), nil
}

func (r *Versions) Insert(ctx context.Context, v domain.Version) error {
	const op = "Post.Versions.Insert"
	doc, err := json.Marshal(v.ContentDoc)
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	q := r.s.q(ctx)
	if err := q.InsertVersion(ctx, sqlc.InsertVersionParams{
		ID: v.ID, PostID: v.PostID, Number: int32(v.Number), Status: string(v.Status), ContentDoc: doc, Body: v.Body,
		TokenCarrier: string(v.TokenCarrier), TokenPosition: string(v.TokenPosition),
		AdvertiserName: v.AdvertiserName, AdvertiserInn: v.AdvertiserINN, LockVersion: int32(v.LockVersion),
		CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	for _, m := range v.Media {
		if err := q.InsertVersionMedia(ctx, sqlc.InsertVersionMediaParams{
			ID: ids.New(), PostVersionID: v.ID, Position: int32(m.Position), MediaFileID: m.MediaFileID,
			MediaDerivativeID: m.DerivativeID, CreatedAt: v.CreatedAt,
		}); err != nil {
			return r.immutable(ctx, op, err)
		}
	}
	r.s.debug(ctx, op, "version_id", v.ID, "number", v.Number, "media_count", len(v.Media))
	return nil
}

func (r *Versions) toVersions(ctx context.Context, op string, rows []sqlc.PostVersion) ([]domain.Version, error) {
	out := make([]domain.Version, 0, len(rows))
	versionIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		var doc domain.Doc
		if err := json.Unmarshal(row.ContentDoc, &doc); err != nil {
			return nil, r.s.fail(ctx, op, fmt.Errorf("decode content_doc: %w", err))
		}
		out = append(out, domain.Version{
			ID: row.ID, PostID: row.PostID, Number: int(row.Number), Status: domain.VersionStatus(row.Status),
			ContentDoc: doc, Body: row.Body, FinalText: str(row.FinalText), Erid: str(row.Erid),
			TokenCarrier: domain.TokenCarrier(row.TokenCarrier), TokenPosition: domain.TokenPosition(row.TokenPosition),
			AdvertiserName: row.AdvertiserName, AdvertiserINN: row.AdvertiserInn,
			ModeratedBy: row.ModeratedBy, ModeratedAt: row.ModeratedAt, RejectionReason: str(row.RejectionReason),
			LockVersion: int(row.LockVersion), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
			Media: []domain.VersionMedia{},
		})
		versionIDs = append(versionIDs, row.ID)
	}
	if len(out) == 0 {
		return out, nil
	}
	media, err := r.s.q(ctx).ListVersionMedia(ctx, versionIDs)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	byID := make(map[uuid.UUID]*domain.Version, len(out))
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	for _, m := range media {
		v := byID[m.PostVersionID]
		v.Media = append(v.Media, domain.VersionMedia{Position: int(m.Position), MediaFileID: m.MediaFileID, DerivativeID: m.MediaDerivativeID})
	}
	return out, nil
}

func (r *Versions) one(ctx context.Context, op string, row sqlc.PostVersion, err error) (domain.Version, error) {
	if err != nil {
		return domain.Version{}, r.s.fail(ctx, op, err)
	}
	vs, err := r.toVersions(ctx, op, []sqlc.PostVersion{row})
	if err != nil {
		return domain.Version{}, err
	}
	return vs[0], nil
}

func (r *Versions) Get(ctx context.Context, id uuid.UUID) (domain.Version, error) {
	row, err := r.s.q(ctx).GetVersion(ctx, id)
	return r.one(ctx, "Post.Versions.Get", row, err)
}

func (r *Versions) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Version, error) {
	row, err := r.s.q(ctx).GetVersionForUpdate(ctx, id)
	return r.one(ctx, "Post.Versions.GetForUpdate", row, err)
}

func (r *Versions) ListByPost(ctx context.Context, postID uuid.UUID) ([]domain.Version, error) {
	const op = "Post.Versions.ListByPost"
	rows, err := r.s.q(ctx).ListVersionsByPost(ctx, postID)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	return r.toVersions(ctx, op, rows)
}

// UpdateStatus сохраняет переход: строка с предыдущей lock_version (04 §1).
func (r *Versions) UpdateStatus(ctx context.Context, v domain.Version) error {
	const op = "Post.Versions.UpdateStatus"
	n, err := r.s.q(ctx).UpdateVersionStatus(ctx, sqlc.UpdateVersionStatusParams{
		ID: v.ID, Status: string(v.Status), FinalText: text(v.FinalText), Erid: text(v.Erid),
		ModeratedBy: v.ModeratedBy, ModeratedAt: v.ModeratedAt, RejectionReason: text(v.RejectionReason),
		LockVersion: int32(v.LockVersion), PrevLockVersion: int32(v.LockVersion - 1),
	})
	if err != nil {
		return r.immutable(ctx, op, err)
	}
	if n == 0 {
		r.s.debug(ctx, op, "version_id", v.ID, "lock_version", v.LockVersion, "result", "stale")
		return domain.ErrStaleVersion
	}
	r.s.debug(ctx, op, "version_id", v.ID, "status", v.Status)
	return nil
}

// Organization — организация-рекламодатель версии (этап «Маркировка», R-8).
func (r *Versions) Organization(ctx context.Context, versionID uuid.UUID) (uuid.UUID, error) {
	id, err := r.s.q(ctx).GetVersionOrganization(ctx, versionID)
	if err != nil {
		return uuid.Nil, r.s.fail(ctx, "Post.Versions.Organization", err)
	}
	return id, nil
}
