package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/postgres/sqlc"
)

// draftFormat — версия формата post.draft.
const draftFormat = 1

// draftJSON — хранимый черновик: документ редактора, файлы по порядку, место токена.
type draftJSON struct {
	V             int         `json:"v"`
	Doc           domain.Doc  `json:"doc"`
	MediaIDs      []uuid.UUID `json:"media_ids"`
	TokenCarrier  string      `json:"token_carrier"`
	TokenPosition string      `json:"token_position"`
}

func encodeDraft(d domain.Draft) ([]byte, error) {
	ids := d.MediaIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return json.Marshal(draftJSON{V: draftFormat, Doc: d.Doc, MediaIDs: ids,
		TokenCarrier: string(d.TokenCarrier), TokenPosition: string(d.TokenPosition)})
}

func decodeDraft(b []byte) (domain.Draft, error) {
	var in draftJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return domain.Draft{}, fmt.Errorf("decode post draft: %w", err)
	}
	if in.V != draftFormat {
		return domain.Draft{}, fmt.Errorf("decode post draft: format %d", in.V)
	}
	return domain.Draft{Doc: in.Doc, MediaIDs: in.MediaIDs,
		TokenCarrier: domain.TokenCarrier(in.TokenCarrier), TokenPosition: domain.TokenPosition(in.TokenPosition)}, nil
}

// Posts — посты кампаний.
type Posts struct{ s *Store }

func (r *Posts) toPost(ctx context.Context, op string, row sqlc.Post) (domain.Post, error) {
	d, err := decodeDraft(row.Draft)
	if err != nil {
		return domain.Post{}, r.s.fail(ctx, op, err)
	}
	return domain.Post{ID: row.ID, OrganizationID: row.OrganizationID, CampaignID: row.CampaignID,
		Status: domain.PostStatus(row.Status), Draft: d, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (r *Posts) Insert(ctx context.Context, p domain.Post) error {
	const op = "Post.Posts.Insert"
	draft, err := encodeDraft(p.Draft)
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	if err := r.s.q(ctx).InsertPost(ctx, sqlc.InsertPostParams{
		ID: p.ID, OrganizationID: p.OrganizationID, CampaignID: p.CampaignID, Status: string(p.Status),
		Draft: draft, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "post_id", p.ID, "campaign_id", p.CampaignID)
	return nil
}

func (r *Posts) GetByCampaign(ctx context.Context, orgID, campaignID uuid.UUID) (domain.Post, error) {
	const op = "Post.Posts.GetByCampaign"
	row, err := r.s.q(ctx).GetPostByCampaign(ctx, sqlc.GetPostByCampaignParams{CampaignID: campaignID, OrganizationID: orgID})
	if err != nil {
		return domain.Post{}, r.s.fail(ctx, op, err)
	}
	return r.toPost(ctx, op, row)
}

func (r *Posts) GetByCampaignForUpdate(ctx context.Context, orgID, campaignID uuid.UUID) (domain.Post, error) {
	const op = "Post.Posts.GetByCampaignForUpdate"
	row, err := r.s.q(ctx).GetPostByCampaignForUpdate(ctx, sqlc.GetPostByCampaignForUpdateParams{CampaignID: campaignID, OrganizationID: orgID})
	if err != nil {
		return domain.Post{}, r.s.fail(ctx, op, err)
	}
	return r.toPost(ctx, op, row)
}

func (r *Posts) SaveDraft(ctx context.Context, p domain.Post) error {
	const op = "Post.Posts.SaveDraft"
	draft, err := encodeDraft(p.Draft)
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	n, err := r.s.q(ctx).SavePostDraft(ctx, sqlc.SavePostDraftParams{ID: p.ID, OrganizationID: p.OrganizationID, Draft: draft, UpdatedAt: p.UpdatedAt})
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	r.s.log.DebugContext(ctx, "post draft saved", "op", op, "post_id", p.ID, "media_count", len(p.Draft.MediaIDs), "doc_bytes", len(draft))
	return nil
}

func (r *Posts) RemoveMediaFromDrafts(ctx context.Context, orgID, fileID uuid.UUID, now time.Time) (int, error) {
	const op = "Post.Posts.RemoveMediaFromDrafts"
	n, err := r.s.q(ctx).RemoveMediaFromDrafts(ctx, sqlc.RemoveMediaFromDraftsParams{FileID: fileID.String(), Now: now, OrganizationID: orgID})
	if err != nil {
		return 0, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "media_file_id", fileID, "drafts_updated", n)
	return int(n), nil
}
