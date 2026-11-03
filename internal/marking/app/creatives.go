package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// VersionChanged — смена состояния версии поста (R-11; порт post/domain.VersionListener через
// переходник). Реагирует только на переход в approved: в той же транзакции — креатив pending,
// цепочка изначального договора и задание отправки креатива (S-17 шаг 3).
func (s *Service) VersionChanged(ctx context.Context, versionID uuid.UUID, to string) error {
	const op = "Marking.VersionChanged"
	if to != domain.VersionStatusApproved {
		return nil
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Creatives.ByVersion(ctx, versionID); err == nil {
			return nil // креатив уже есть (повтор)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		content, err := s.Posts.CreativeContent(ctx, versionID)
		if err != nil {
			return err
		}
		if _, err := s.EnsureInitialChain(ctx, content.OrganizationID); err != nil {
			return err
		}
		c, created, err := s.Creatives.Insert(ctx, domain.NewCreative(ids.New(), versionID, s.Clock.Now()))
		if err != nil || !created {
			return err
		}
		s.log.DebugContext(ctx, "creative created for approved version", logx.KeyOp, op, KeyCreativeID, c.ID, KeyVersionID, versionID)
		return s.enqueueSync(ctx, op, c.Ref(), reasonNew)
	})
}

// buildCreative — запрос креатива (R-8): версия в approved (R-11), цепочка готова (R-4),
// изображения в публичной области (R-9). После erid креатив не отправляется (D-03).
func (s *Service) buildCreative(ctx context.Context, r *creativeRecord) (*outgoing, error, error) {
	const op = "Marking.Sync"
	c := r.v
	if c.HasErid() {
		s.log.DebugContext(ctx, "creative has erid, not sent again", logx.KeyOp, op, KeyCreativeID, c.ID)
		return nil, nil, nil
	}
	content, err := s.Posts.CreativeContent(ctx, c.PostVersionID)
	if err != nil {
		return nil, nil, err
	}
	if content.VersionStatus != domain.VersionStatusApproved {
		// R-11: версия стала ready в обход маркировки или отклонена — креатив без отправки удаляется.
		s.log.DebugContext(ctx, "creative skipped, version not approved", logx.KeyOp, op, KeyCreativeID, c.ID,
			KeyVersionID, c.PostVersionID, "status", content.VersionStatus)
		if c.Sync.SentAt.IsZero() {
			if err := s.Creatives.Delete(ctx, c.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return nil, nil, err
			}
		}
		return nil, nil, nil
	}
	platform, err := s.platformOrganization(ctx)
	if err != nil {
		return nil, nil, err
	}
	advertiser, err := s.ensureOrganization(ctx, content.OrganizationID, false)
	if err != nil {
		return nil, nil, err
	}
	contract, err := s.ensureContract(ctx, domain.ContractInitial, platform.OrganizationID, content.OrganizationID)
	if err != nil {
		return nil, nil, err
	}
	v := domain.DependencyVerdict([]domain.Dependency{
		dependency(domain.NeedPlatformOrganization, platform.Ref(), platform.Sync),
		dependency(domain.NeedAdvertiserOrganization, advertiser.Ref(), advertiser.Sync),
		dependency(domain.NeedInitialContract, contract.Ref(), contract.Sync),
	})
	if !v.Ready() {
		return nil, s.wait(ctx, op, r.ref(), v), nil
	}
	if n := content.PendingImages(); n > 0 {
		s.log.DebugContext(ctx, "creative waits for media", logx.KeyOp, op, KeyCreativeID, c.ID, "pending_images", n)
		return nil, job.Postpone(fmt.Errorf("%w: %d images are not public yet", errDependencyWait, n), s.Config.DependencyWait), nil
	}
	r.v.ContractID = contract.ID
	req := domain.NewCreativeRequest(r.v, contract.ExternalID(), content, s.Config.KKTUCode)
	hash, err := domain.PayloadHash(req)
	if err != nil {
		return nil, nil, err
	}
	s.log.InfoContext(ctx, "creative registration started", logx.KeyOp, op, KeyCreativeID, c.ID, KeyVersionID, c.PostVersionID,
		"images", len(req.MediaData), "urls", len(req.URLs))
	return &outgoing{
		ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertCreative(ctx, req)
		},
	}, nil, nil
}

// creativeStatus — последствия статуса креатива в транзакции: первый токен — erid и версия в
// ready (R-8); итог ЕРИР — erir_status; финальная ошибка после erid — отклонение (R-12).
func (s *Service) creativeStatus(ctx context.Context, r *creativeRecord, o domain.Outcome, token string, now time.Time) error {
	const op = "Marking.CreativeStatus"
	c := &r.v
	hadErid := c.HasErid()
	c.ApplyErir(o)
	if o == domain.OutcomeFailed {
		if hadErid {
			return s.creativeRejected(ctx, c)
		}
		return nil
	}
	if !c.SetErid(token, now) {
		return nil
	}
	wait := now.Sub(c.CreatedAt)
	s.Metrics.EridWait(wait)
	s.log.InfoContext(ctx, "erid received", logx.KeyOp, op, KeyCreativeID, c.ID, KeyVersionID, c.PostVersionID,
		"erid", c.Erid, "wait_ms", wait.Milliseconds())
	if err := s.Versions.MarkVersionReady(ctx, c.PostVersionID, c.Erid); err != nil {
		// Итоговый текст не собирается (final_text_too_long): креатив остаётся с erid, версия
		// в approved — решает администратор. Прочие ошибки откатывают шаг.
		if !finalTextTooLong(err) {
			return err
		}
		s.log.ErrorContext(ctx, "post version ready failed after erid", logx.KeyOp, op, KeyCreativeID, c.ID,
			KeyVersionID, c.PostVersionID, logx.KeyError, err.Error())
		s.raise(ctx, op, alert.Alert{
			Reason: alertEridAfterReady, ObjectKey: c.PostVersionID.String(), Level: alert.LevelError,
			Summary: fmt.Sprintf("Post version %s got erid but could not become ready", c.PostVersionID),
			Fields:  map[string]any{"post_version_id": c.PostVersionID.String(), "creative_id": c.ID.String()},
		})
	}
	return nil
}

// creativeRejected — ЕРИР отклонил креатив с erid (S-28 шаг 2, R-12): модуль размещений
// возвращает неопубликованные размещения версии; оповещение администратору.
func (s *Service) creativeRejected(ctx context.Context, c *domain.Creative) error {
	const op = "Marking.CreativeRejected"
	published, err := s.Listener.CreativeRejected(ctx, c.PostVersionID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(published))
	for _, id := range published {
		ids = append(ids, id.String())
	}
	s.log.ErrorContext(ctx, "erir rejected creative", logx.KeyOp, op, KeyCreativeID, c.ID, KeyVersionID, c.PostVersionID,
		KeyOrdStatus, c.Sync.OrdStatus, "published_placements", len(ids))
	s.raise(ctx, op, alert.Alert{
		Reason: alertCreativeRejected, ObjectKey: c.ID.String(), Level: alert.LevelError,
		Summary: fmt.Sprintf("ERIR rejected creative %s of post version %s", c.ID, c.PostVersionID),
		Fields: map[string]any{"creative_id": c.ID.String(), "post_version_id": c.PostVersionID.String(),
			"ord_status": c.Sync.OrdStatus, "published_placement_ids": ids},
	})
	return nil
}

// finalTextTooLong — отказ post/app.MarkVersionReady: итоговый текст с erid длиннее предела.
func finalTextTooLong(err error) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == apperr.CodeConflict && e.Details["reason"] == "final_text_too_long"
}
