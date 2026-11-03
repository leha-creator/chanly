package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// CreativeContent — версия поста для креатива в ОРД (этап «Маркировка», R-8, R-9).
type CreativeContent struct {
	VersionID      uuid.UUID
	Status         domain.VersionStatus
	OrganizationID uuid.UUID
	// Text — текст body без разметки.
	Text string
	// URLs — адреса ссылок body по порядку, с UTM-метками заказа.
	URLs []string
	// ImageURLs — публичные ссылки изображений по порядку; пусто — производная ещё не опубликована.
	ImageURLs []string
}

// CreativeContent читает версию для креатива. Текст поста в журнал не пишется (07 §5.4).
func (s *Service) CreativeContent(ctx context.Context, versionID uuid.UUID) (CreativeContent, error) {
	const op = "Post.CreativeContent"
	view, err := s.GetVersion(ctx, versionID)
	if err != nil {
		return CreativeContent{}, err
	}
	org, err := s.Versions.Organization(ctx, versionID)
	if err != nil {
		return CreativeContent{}, notFound(err, "post version")
	}
	v := view.Version
	out := CreativeContent{
		VersionID: v.ID, Status: v.Status, OrganizationID: org,
		Text: domain.BodyPlainText(v.Body), URLs: domain.BodyLinkURLs(v.Body), ImageURLs: view.MediaURLs,
	}
	pending := 0
	for _, u := range out.ImageURLs {
		if u == "" {
			pending++
		}
	}
	s.log.DebugContext(ctx, "creative content read", logx.KeyOp, op, KeyVersionID, v.ID, "status", v.Status,
		"images", len(out.ImageURLs), "pending_images", pending, "urls", len(out.URLs))
	return out, nil
}
