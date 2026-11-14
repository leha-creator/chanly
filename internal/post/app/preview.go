package app

import (
	"context"
	"math"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// PreviewView — предпросмотр поста (S-05).
type PreviewView struct {
	Body            string
	BodyLength      int
	BodyLimit       int
	FinalTextSample string
	MediaURLs       []string
	Problems        []domain.Problem
}

// Preview сериализует сохранённый черновик с заглушкой UTM (R-12), собирает пример
// итогового текста с условным erid и проверяет черновик (R-13). Изображения — готовые
// производные для канала; недостающие ставятся к построению и дают media_processing (R-6).
func (s *Service) Preview(ctx context.Context, orgID, campaignID uuid.UUID) (PreviewView, error) {
	const op = "Post.Preview"
	p, err := s.Posts.GetByCampaign(ctx, orgID, campaignID)
	if err != nil {
		return PreviewView{}, notFound(err, "campaign")
	}
	d := p.Draft
	items, req, canBuild, err := s.draftRequirements(ctx, orgID, d)
	if err != nil {
		return PreviewView{}, err
	}
	urls, ready, err := s.channelMedia(ctx, orgID, items, canBuild)
	if err != nil {
		return PreviewView{}, err
	}

	body := domain.Serialize(d.Doc, &domain.PreviewUTM)
	sample, _ := domain.FinalText(body, domain.Marking{Carrier: d.TokenCarrier, Position: d.TokenPosition,
		Name: req.Name, INN: req.INN}, domain.SampleErid, math.MaxInt)
	problems := domain.Problems(d, body, domain.ProblemContext{
		BodyLimit: s.Config.BodyLimit, FinalTextLimit: s.Config.FinalTextLimit, AlbumLimit: s.Config.AlbumLimit,
		MediaReady: ready, Requisites: req,
	})
	v := PreviewView{Body: body, BodyLength: domain.Length(body), BodyLimit: s.Config.BodyLimit,
		FinalTextSample: sample, MediaURLs: urls, Problems: problems}
	s.log.DebugContext(ctx, "post preview built", logx.KeyOp, op, KeyPostID, p.ID, "body_length", v.BodyLength,
		"problems", domain.Codes(problems))
	return v, nil
}

// channelMedia returns the complete ordered album or no URLs while processing.
func (s *Service) channelMedia(ctx context.Context, orgID uuid.UUID, items []albumRequirement, canBuild bool) ([]string, bool, error) {
	urls := []string{}
	selected, missing, ready, err := s.albumDerivatives(ctx, orgID, items, canBuild)
	if err != nil {
		return nil, false, err
	}
	if len(missing) > 0 {
		// Recheck live files under the SaveDraft/Delete lock before enqueuing.
		err = s.Tx.InTx(ctx, func(ctx context.Context) error {
			if err := s.Posts.LockDraftMedia(ctx, orgID); err != nil {
				return err
			}
			ids := make([]uuid.UUID, len(missing))
			for i, item := range missing {
				ids[i] = item.FileID
			}
			live, err := s.MediaFiles.LiveSubset(ctx, orgID, ids)
			if err != nil {
				return err
			}
			liveIDs := make(map[uuid.UUID]bool, len(live))
			for _, f := range live {
				liveIDs[f.ID] = true
			}
			for _, item := range missing {
				if !liveIDs[item.FileID] {
					continue
				}
				if err := s.ensureDerivative(ctx, orgID, item.FileID, item.Params); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}
	if !ready {
		return urls, false, nil
	}
	for _, d := range selected {
		u, err := s.Storage.PresignGet(ctx, d.StorageKey, s.Config.URLTTL)
		if err != nil {
			return nil, false, err
		}
		urls = append(urls, u)
	}
	return urls, true, nil
}
