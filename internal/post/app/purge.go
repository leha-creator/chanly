package app

import (
	"context"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// PurgeMedia — обработчик purge_media (R-19, раз в сутки): удаляет из хранилища
// оригиналы, удалённые больше RetentionDeleted назад и не входящие ни в одну версию,
// и производные старше того же срока вне версий. Порции по 100; объект, которого
// уже нет, считается удалённым. Неудачное удаление остаётся до следующего запуска.
func (s *Service) PurgeMedia(ctx context.Context) error {
	const op = "Post.PurgeMedia"
	before := s.Clock.Now().Add(-s.Config.RetentionDeleted)
	files, derivatives := 0, 0
	for {
		batch, err := s.MediaFiles.ForPurge(ctx, before, purgeBatch)
		if err != nil {
			return err
		}
		failed := false
		for _, f := range batch {
			if err := s.Storage.Delete(ctx, f.StorageKey); err != nil {
				s.log.WarnContext(ctx, "media purge object failed", logx.KeyOp, op, "key", f.StorageKey, logx.KeyError, err.Error())
				failed = true
				continue
			}
			if err := s.MediaFiles.MarkPurged(ctx, f.ID, s.Clock.Now()); err != nil {
				return err
			}
			files++
		}
		if failed || len(batch) < purgeBatch {
			break
		}
	}
	for {
		batch, err := s.Derivatives.ForPurge(ctx, before, purgeBatch)
		if err != nil {
			return err
		}
		failed := false
		for _, d := range batch {
			if err := s.Storage.Delete(ctx, d.StorageKey); err != nil {
				s.log.WarnContext(ctx, "media purge object failed", logx.KeyOp, op, "key", d.StorageKey, logx.KeyError, err.Error())
				failed = true
				continue
			}
			if err := s.Derivatives.MarkPurged(ctx, d.ID, s.Clock.Now()); err != nil {
				return err
			}
			derivatives++
		}
		if failed || len(batch) < purgeBatch {
			break
		}
	}
	s.log.InfoContext(ctx, "media purged", logx.KeyOp, op, "files", files, "derivatives", derivatives)
	return nil
}
