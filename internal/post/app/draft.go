package app

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// DraftView — черновик поста с файлами по порядку.
type DraftView struct {
	Post  domain.Post
	Media []MediaView
}

// DraftInput — черновик от кабинета; документ ещё не нормализован.
type DraftInput struct {
	Doc           domain.Doc
	MediaIDs      []uuid.UUID
	TokenCarrier  *domain.TokenCarrier
	TokenPosition *domain.TokenPosition
}

// GetDraft — черновик поста кампании.
func (s *Service) GetDraft(ctx context.Context, orgID, campaignID uuid.UUID) (DraftView, error) {
	p, err := s.Posts.GetByCampaign(ctx, orgID, campaignID)
	if err != nil {
		return DraftView{}, notFound(err, "campaign")
	}
	return s.draftView(ctx, p)
}

// SaveDraft сохраняет черновик (S-04): нормализация документа и распознавание ссылок
// (R-8, R-9), проверка файлов организации (R-15), производные для канала с текущими
// реквизитами (R-6, R-14). Недоступное место токена не отклоняется (R-13).
func (s *Service) SaveDraft(ctx context.Context, orgID, userID, campaignID uuid.UUID, in DraftInput) (DraftView, error) {
	const op = "Post.SaveDraft"
	doc, err := domain.Normalize(in.Doc, s.Config.Doc)
	if err != nil {
		var de *domain.DocError
		if errors.As(err, &de) {
			s.log.WarnContext(ctx, "post draft rejected", logx.KeyOp, op, KeyCampaignID, campaignID,
				"reason", domain.ReasonInvalidDoc, "rule", de.Rule, "path", de.Path)
		}
		return DraftView{}, ruleError(err)
	}
	doc, autolinked := domain.Autolink(doc)

	var saved domain.Post
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Posts.LockDraftMedia(ctx, orgID); err != nil {
			return err
		}
		p, err := s.Posts.GetByCampaignForUpdate(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		// R-19 этапа «Заказ и размещения»: у архивной кампании черновик не меняется.
		c, err := s.Campaigns.Get(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		if c.Status == domain.CampaignArchived {
			s.log.WarnContext(ctx, "post draft rejected", logx.KeyOp, op, KeyPostID, p.ID, "reason", reasonArchived)
			return apperr.Conflict("campaign is archived").WithDetail("reason", reasonArchived).WithCause(domain.ErrArchived)
		}
		now := s.Clock.Now()
		if err := p.SaveDraft(domain.DraftInput{Doc: doc, MediaIDs: in.MediaIDs, TokenCarrier: in.TokenCarrier,
			TokenPosition: in.TokenPosition}, s.Config.AlbumLimit, now); err != nil {
			s.rejectDraft(ctx, op, p.ID, err, "")
			return ruleError(err)
		}
		if missing, err := s.missingMedia(ctx, orgID, p.Draft.MediaIDs); err != nil {
			return err
		} else if missing != uuid.Nil {
			s.rejectDraft(ctx, op, p.ID, domain.ErrMediaNotFound, "")
			return apperr.Validation(domain.ErrMediaNotFound.Field, domain.ErrMediaNotFound.Reason).
				WithDetail("media_file_id", missing.String())
		}
		if err := s.ensureDraftDerivatives(ctx, orgID, p.Draft); err != nil {
			return err
		}
		if err := s.Posts.SaveDraft(ctx, p); err != nil {
			return err
		}
		saved = p
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionPostDraftSaved, ObjectType: "post", ObjectID: p.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"campaign_id": campaignID, "media_count": len(p.Draft.MediaIDs),
				"token_carrier": p.Draft.TokenCarrier, "token_position": p.Draft.TokenPosition},
		})
	})
	if err != nil {
		return DraftView{}, err
	}
	s.log.InfoContext(ctx, "post draft saved", logx.KeyOp, op, KeyPostID, saved.ID, "media_count", len(saved.Draft.MediaIDs),
		"body_length", domain.Length(domain.Serialize(saved.Draft.Doc, nil)), "links_autolinked", autolinked)
	return s.draftView(ctx, saved)
}

func (s *Service) rejectDraft(ctx context.Context, op string, postID uuid.UUID, err error, path string) {
	var re *domain.RuleError
	if errors.As(err, &re) {
		s.log.WarnContext(ctx, "post draft rejected", logx.KeyOp, op, KeyPostID, postID, "reason", re.Reason, "path", path)
	}
}

// missingMedia — первый файл из ids, которого нет среди живых файлов организации; uuid.Nil — все есть.
func (s *Service) missingMedia(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (uuid.UUID, error) {
	if len(ids) == 0 {
		return uuid.Nil, nil
	}
	live, err := s.MediaFiles.LiveSubset(ctx, orgID, ids)
	if err != nil {
		return uuid.Nil, err
	}
	for _, id := range ids {
		if !slices.ContainsFunc(live, func(f domain.MediaFile) bool { return f.ID == id }) {
			return id, nil
		}
	}
	return uuid.Nil, nil
}

// ensureDraftDerivatives queues only the variants selected for the saved positions.
func (s *Service) ensureDraftDerivatives(ctx context.Context, orgID uuid.UUID, d domain.Draft) error {
	if len(d.MediaIDs) == 0 {
		return nil
	}
	items, _, canBuild, err := s.draftRequirements(ctx, orgID, d)
	if err != nil || !canBuild {
		return err
	}
	for _, item := range items {
		if err := s.ensureDerivative(ctx, orgID, item.FileID, item.Params); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) draftView(ctx context.Context, p domain.Post) (DraftView, error) {
	v := DraftView{Post: p, Media: []MediaView{}}
	if len(p.Draft.MediaIDs) == 0 {
		return v, nil
	}
	live, err := s.MediaFiles.LiveSubset(ctx, p.OrganizationID, p.Draft.MediaIDs)
	if err != nil {
		return DraftView{}, err
	}
	byID := make(map[uuid.UUID]domain.MediaFile, len(live))
	for _, f := range live {
		byID[f.ID] = f
	}
	ordered := make([]domain.MediaFile, 0, len(live))
	for _, id := range p.Draft.MediaIDs {
		if f, ok := byID[id]; ok {
			ordered = append(ordered, f)
		}
	}
	if v.Media, err = s.mediaViews(ctx, ordered); err != nil {
		return DraftView{}, err
	}
	return v, nil
}
