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
	params, req, canBuild, err := s.draftParams(ctx, orgID, d)
	if err != nil {
		return PreviewView{}, err
	}
	urls, ready, err := s.channelMedia(ctx, orgID, d.MediaIDs, params, canBuild)
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

// channelMedia — подписанные ссылки готовых производных для канала по порядку
// альбома и признак, что готовы все. Неготовые ставятся к построению (идемпотентно).
func (s *Service) channelMedia(ctx context.Context, orgID uuid.UUID, fileIDs []uuid.UUID, params domain.DerivativeParams,
	canBuild bool) ([]string, bool, error) {
	urls := []string{}
	if len(fileIDs) == 0 {
		return urls, true, nil
	}
	if !canBuild {
		return urls, false, nil
	}
	live, err := s.MediaFiles.LiveSubset(ctx, orgID, fileIDs)
	if err != nil {
		return nil, false, err
	}
	liveIDs := make(map[uuid.UUID]bool, len(live))
	for _, f := range live {
		liveIDs[f.ID] = true
	}
	list, err := s.Derivatives.ListByFiles(ctx, fileIDs, params.Kind)
	if err != nil {
		return nil, false, err
	}
	hash := params.Hash()
	byFile := make(map[uuid.UUID]domain.Derivative, len(list))
	for _, d := range list {
		if d.ParamsHash == hash {
			byFile[d.MediaFileID] = d
		}
	}
	ready := true
	var missing []uuid.UUID
	for _, id := range fileIDs {
		if !liveIDs[id] {
			continue
		}
		d, ok := byFile[id]
		if !ok || !d.Ready() {
			ready = false
			missing = append(missing, id)
			continue
		}
		u, err := s.Storage.PresignGet(ctx, d.StorageKey, s.Config.URLTTL)
		if err != nil {
			return nil, false, err
		}
		urls = append(urls, u)
	}
	if len(missing) > 0 {
		// Производной нет (реквизиты сменились без сохранения) или её очистил purge_media.
		err := s.Tx.InTx(ctx, func(ctx context.Context) error {
			for _, id := range missing {
				if err := s.ensureDerivative(ctx, orgID, id, params); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}
	return urls, ready, nil
}
