package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// PublicationMedia — изображение версии для публикации в канал: производная по порядку.
type PublicationMedia struct {
	DerivativeID uuid.UUID
	ContentType  string
	Size         int64
}

// PublicationContent — версия поста для публикации (этап «Автопостинг и контроль», R-1, R-3).
type PublicationContent struct {
	Version domain.Version
	// MarkingOK — итоговый текст совпал с собранным заново (I-01).
	MarkingOK bool
	Media     []PublicationMedia
}

// PublicationContent — итоговый текст версии, проверка маркировки и изображения по порядку.
// Текст поста в журнал не пишется (07 §5.4).
func (s *Service) PublicationContent(ctx context.Context, versionID uuid.UUID) (PublicationContent, error) {
	const op = "Post.PublicationContent"
	v, err := s.Versions.Get(ctx, versionID)
	if err != nil {
		return PublicationContent{}, notFound(err, "post version")
	}
	out := PublicationContent{Version: v, MarkingOK: domain.VerifyFinalText(v, s.Config.FinalTextLimit) == nil}
	if !out.MarkingOK && v.Status == domain.VersionReady {
		s.log.WarnContext(ctx, "final text mismatch", logx.KeyOp, op, KeyVersionID, v.ID)
	}
	for _, m := range v.Media {
		d, err := s.Derivatives.Get(ctx, m.DerivativeID)
		if err != nil {
			return PublicationContent{}, fmt.Errorf("version %s derivative %s: %w", v.ID, m.DerivativeID, err)
		}
		out.Media = append(out.Media, PublicationMedia{DerivativeID: d.ID, ContentType: d.Mime, Size: d.SizeBytes})
	}
	s.log.DebugContext(ctx, "publication content read", logx.KeyOp, op, KeyVersionID, v.ID, "status", v.Status,
		"marking_ok", out.MarkingOK, "images", len(out.Media))
	return out, nil
}

// OpenDerivative открывает файл производной из закрытой области хранилища (R-16): публикация
// не зависит от публичной ссылки. Закрывает вызывающий. Удалённая производная — Permanent.
func (s *Service) OpenDerivative(ctx context.Context, derivativeID uuid.UUID) (io.ReadCloser, error) {
	const op = "Post.OpenDerivative"
	d, err := s.Derivatives.Get(ctx, derivativeID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, job.Permanent(err)
	}
	if err != nil {
		return nil, err
	}
	if !d.Ready() {
		return nil, job.Permanent(fmt.Errorf("derivative %s is not ready", d.ID))
	}
	r, _, err := s.Storage.Get(ctx, d.StorageKey)
	if err != nil {
		return nil, err
	}
	s.log.DebugContext(ctx, "derivative opened", logx.KeyOp, op, KeyDerivativeID, d.ID)
	return r, nil
}
