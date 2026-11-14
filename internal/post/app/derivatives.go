package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// buildDedupeKey — dedupe_key задания построения (R-6): media:{id}:{kind}:{hash}.
func buildDedupeKey(fileID uuid.UUID, kind domain.DerivativeKind, hash string) string {
	return fmt.Sprintf("media:%s:%s:%s", fileID, kind, hash)
}

// ensureDerivative создаёт производную в pending (или находит готовую) и, если
// она не готова, ставит задание построения. Работает в транзакции из context.
func (s *Service) ensureDerivative(ctx context.Context, orgID, fileID uuid.UUID, p domain.DerivativeParams) error {
	hash := p.Hash()
	d, pending, err := s.Derivatives.Ensure(ctx, fileID, p.Kind, hash, s.Clock.Now())
	if err == nil {
		s.log.DebugContext(ctx, "album derivative ensured", logx.KeyOp, "Post.EnsureDerivative", KeyDerivativeID, d.ID,
			"kind", p.Kind, "render_version", p.Version(), "pending", pending)
	}
	if err != nil || !pending {
		return err
	}
	return s.Jobs.Enqueue(ctx, job.Job{
		Kind: KindBuildMediaDerivative, DedupeKey: buildDedupeKey(fileID, p.Kind, hash), MaxAttempts: buildAttempts,
		Payload: BuildPayload{DerivativeID: d.ID, OrganizationID: orgID},
	})
}

// BuildDerivative — обработчик build_media_derivative (R-6). Идемпотентен: готовая
// производная, очищенный оригинал и устаревшие реквизиты плашки — выход без действий.
// Неустранимая ошибка построения — job.Permanent.
func (s *Service) BuildDerivative(ctx context.Context, p BuildPayload) error {
	const op = "Post.BuildDerivative"
	start := s.Clock.Now()
	d, err := s.Derivatives.Get(ctx, p.DerivativeID)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	skip := func(reason string) error {
		s.log.InfoContext(ctx, "media derivative skipped", logx.KeyOp, op, KeyDerivativeID, d.ID, "kind", d.Kind, "reason", reason)
		return nil
	}
	if d.Ready() {
		return skip(derivativeSkipAlreadyReady)
	}
	f, err := s.MediaFiles.GetAny(ctx, d.MediaFileID)
	if err != nil {
		return err
	}
	if f.PurgedAt != nil {
		return skip("file_purged")
	}
	params := domain.NewDerivativeParams(d.Kind, "", "")
	if d.Kind == domain.KindLabeled {
		req, err := s.Requisites.Requisites(ctx, p.OrganizationID)
		if err != nil {
			return err
		}
		params = domain.NewDerivativeParams(d.Kind, req.Name, req.INN)
	}
	if params.Hash() != d.ParamsHash {
		// Реквизиты или версия шаблона сменились: нужную производную поставит следующее сохранение.
		return skip("stale_params")
	}

	rc, _, err := s.Storage.Get(ctx, f.StorageKey)
	if err != nil {
		return err
	}
	original, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("read original: %w", err)
	}
	out, err := s.Renderer.Render(ctx, original, params)
	if err != nil {
		if errors.Is(err, domain.ErrUndecodable) || errors.Is(err, domain.ErrDerivativeTooLarge) || errors.Is(err, domain.ErrPlateLayout) {
			s.log.ErrorContext(ctx, "media derivative failed", logx.KeyOp, op, KeyMediaFileID, f.ID, "kind", d.Kind,
				KeyDerivativeID, d.ID, logx.KeyError, err.Error())
			return job.Permanent(err)
		}
		return err
	}

	key := derivativeKeyArea + "/" + ids.New().String() + domain.Ext(out.Mime)
	if err := s.Storage.PutPrivate(ctx, key, bytes.NewReader(out.Data), int64(len(out.Data)), out.Mime); err != nil {
		return err
	}
	d.StorageKey, d.Mime, d.Width, d.Height, d.SizeBytes = key, out.Mime, out.Width, out.Height, int64(len(out.Data))
	var updated bool
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		updated, err = s.Derivatives.MarkReady(ctx, d)
		return err
	})
	if err != nil || !updated {
		// Параллельное построение успело раньше или запись не сохранилась: наш объект лишний.
		s.deleteOrphan(ctx, op, key)
		if err != nil {
			return err
		}
		return skip(derivativeSkipAlreadyReady)
	}
	s.log.InfoContext(ctx, "media derivative built", logx.KeyOp, op, KeyMediaFileID, f.ID, "kind", d.Kind,
		KeyDerivativeID, d.ID, "duration_ms", s.Clock.Now().Sub(start).Milliseconds())
	return nil
}
