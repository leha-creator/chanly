package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// PublishDerivative — обработчик publish_media_derivative (R-7): копия производной
// в публичной области и её постоянная ссылка. Опубликованная — выход без действий.
// Копирование — вне транзакции; отметка — в транзакции.
func (s *Service) PublishDerivative(ctx context.Context, derivativeID uuid.UUID) error {
	const op = "Post.PublishDerivative"
	d, err := s.Derivatives.Get(ctx, derivativeID)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	if d.PublishedAt != nil {
		s.log.InfoContext(ctx, "media derivative skipped", logx.KeyOp, op, KeyDerivativeID, d.ID, "reason", "already_published")
		return nil
	}
	if !d.Ready() {
		return fmt.Errorf("derivative %s is not ready", d.ID)
	}
	key := s.Storage.NewPublicKey(domain.Ext(d.Mime))
	if err := s.Storage.CopyToPublic(ctx, d.StorageKey, key, d.Mime); err != nil {
		return err
	}
	url := s.Storage.PublicURL(key)
	var updated bool
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		updated, err = s.Derivatives.MarkPublished(ctx, d.ID, key, url, s.Clock.Now())
		return err
	})
	if err != nil {
		return err
	}
	if !updated {
		s.log.InfoContext(ctx, "media derivative skipped", logx.KeyOp, op, KeyDerivativeID, d.ID, "reason", "already_published")
		return nil
	}
	s.log.InfoContext(ctx, "media derivative published", logx.KeyOp, op, KeyDerivativeID, d.ID)
	return nil
}
