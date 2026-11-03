package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// VersionView — версия поста с публичными ссылками изображений (для ОРД и публикации).
type VersionView struct {
	Version domain.Version
	// MediaURLs — публичные ссылки по порядку; пусто у изображения, которое ещё не опубликовано.
	MediaURLs []string
}

// publishDedupeKey — dedupe_key задания публикации (R-19).
func publishDedupeKey(derivativeID uuid.UUID) string { return "media_publish:" + derivativeID.String() }

// CreateVersion создаёт следующую версию поста из черновика кампании (R-18) — для
// оформления заказа и правки после него (этап «Заказ и размещения»). Работает в
// транзакции вызывающего (открывает свою, если её нет). В черновике не должно быть
// проблем, все производные для канала должны быть готовы — иначе 409 post_not_ready
// с кодами проблем. Ставит публикацию производных в публичной области (R-7).
func (s *Service) CreateVersion(ctx context.Context, orgID, campaignID, userID uuid.UUID, orderNumber int64) (domain.Version, error) {
	const op = "Post.CreateVersion"
	var v domain.Version
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Posts.GetByCampaignForUpdate(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		d := p.Draft
		params, req, canBuild, err := s.draftParams(ctx, orgID, d)
		if err != nil {
			return err
		}
		derivs, ready, err := s.readyDerivatives(ctx, d.MediaIDs, params, canBuild)
		if err != nil {
			return err
		}
		utm := domain.UTMForOrder(orderNumber)
		body := domain.Serialize(d.Doc, &utm)
		problems := domain.Problems(d, body, domain.ProblemContext{
			BodyLimit: s.Config.BodyLimit, FinalTextLimit: s.Config.FinalTextLimit, AlbumLimit: s.Config.AlbumLimit,
			MediaReady: ready, Requisites: req,
		})
		if len(problems) > 0 {
			s.log.WarnContext(ctx, "post version blocked", logx.KeyOp, op, KeyPostID, p.ID, "problems", domain.Codes(problems))
			return apperr.Conflict("post is not ready").WithDetail("reason", reasonPostNotReady).
				WithDetail(problemsDetail, domain.Codes(problems))
		}

		number, err := s.Versions.NextNumber(ctx, p.ID)
		if err != nil {
			return err
		}
		media := make([]domain.VersionMedia, 0, len(d.MediaIDs))
		for i, id := range d.MediaIDs {
			media = append(media, domain.VersionMedia{Position: i, MediaFileID: id, DerivativeID: derivs[id].ID})
		}
		if v, err = domain.NewVersion(ids.New(), p, number, req, body, media, userID, s.Clock.Now()); err != nil {
			return err
		}
		if err := s.Versions.Insert(ctx, v); err != nil {
			return err
		}
		for _, m := range media {
			if err := s.Jobs.Enqueue(ctx, job.Job{Kind: KindPublishMediaDerivative, DedupeKey: publishDedupeKey(m.DerivativeID),
				MaxAttempts: publishAttempts, Payload: PublishPayload{DerivativeID: m.DerivativeID}}); err != nil {
				return err
			}
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionPostVersionCreated, ObjectType: "post_version", ObjectID: v.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"post_id": p.ID, "number": number, "order_number": orderNumber, "media_count": len(media),
				"token_carrier": v.TokenCarrier, "token_position": v.TokenPosition},
		})
	})
	if err != nil {
		return domain.Version{}, err
	}
	s.log.InfoContext(ctx, "post version created", logx.KeyOp, op, KeyPostID, v.PostID, KeyVersionID, v.ID,
		"number", v.Number, "media_count", len(v.Media), "body_length", domain.Length(v.Body))
	return v, nil
}

// readyDerivatives — готовые производные для канала по файлам и признак, что готовы все.
func (s *Service) readyDerivatives(ctx context.Context, fileIDs []uuid.UUID, params domain.DerivativeParams,
	canBuild bool) (map[uuid.UUID]domain.Derivative, bool, error) {
	out := map[uuid.UUID]domain.Derivative{}
	if len(fileIDs) == 0 {
		return out, true, nil
	}
	if !canBuild {
		return out, false, nil
	}
	list, err := s.Derivatives.ListByFiles(ctx, fileIDs, params.Kind)
	if err != nil {
		return nil, false, err
	}
	hash := params.Hash()
	for _, d := range list {
		if d.ParamsHash == hash && d.Ready() {
			out[d.MediaFileID] = d
		}
	}
	return out, len(out) == len(fileIDs), nil
}

// ApproveVersion — администратор одобрил версию (S-17). Роль проверяет вызывающий этап.
func (s *Service) ApproveVersion(ctx context.Context, versionID, adminID uuid.UUID) (domain.Version, error) {
	return s.transition(ctx, "Post.ApproveVersion", versionID, domain.TransitionApprove, actionPostVersionApproved,
		audit.ActorAdmin, func(v *domain.Version) error { return v.Approve(adminID, s.Clock.Now()) })
}

// RejectVersion — администратор отклонил версию с причиной (S-17).
func (s *Service) RejectVersion(ctx context.Context, versionID, adminID uuid.UUID, reason string) (domain.Version, error) {
	return s.transition(ctx, "Post.RejectVersion", versionID, domain.TransitionReject, actionPostVersionRejected,
		audit.ActorAdmin, func(v *domain.Version) error { return v.Reject(adminID, reason, s.Clock.Now()) })
}

// MarkVersionReady — получен erid (этап «Маркировка»): собирается итоговый текст (05 §2.5).
func (s *Service) MarkVersionReady(ctx context.Context, versionID uuid.UUID, erid string) (domain.Version, error) {
	return s.transition(ctx, "Post.MarkVersionReady", versionID, domain.TransitionMarkReady, actionPostVersionReady,
		audit.ActorSystem, func(v *domain.Version) error { return v.MarkReady(erid, s.Config.FinalTextLimit) })
}

// transition применяет переход версии под блокировкой строки.
func (s *Service) transition(ctx context.Context, op string, versionID uuid.UUID, name, action, actor string,
	apply func(*domain.Version) error) (domain.Version, error) {
	var v domain.Version
	var from domain.VersionStatus
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if v, err = s.Versions.GetForUpdate(ctx, versionID); err != nil {
			return notFound(err, "post version")
		}
		from = v.Status
		if err := apply(&v); err != nil {
			switch {
			case apperr.Is(err, apperr.CodeInvalidTransition):
				s.log.WarnContext(ctx, "post version transition rejected", logx.KeyOp, op, KeyVersionID, versionID,
					"transition", name, "status", from)
				return err
			case errors.Is(err, domain.ErrFinalTextTooLong):
				s.log.ErrorContext(ctx, "post final text too long", logx.KeyOp, op, KeyVersionID, versionID,
					"length", domain.Length(v.Body))
				return apperr.Conflict("final text exceeds the limit").WithDetail("reason", "final_text_too_long").WithCause(err)
			}
			return ruleError(err)
		}
		if err := s.Versions.UpdateStatus(ctx, v); err != nil {
			if errors.Is(err, domain.ErrStaleVersion) {
				return apperr.Conflict("post version was changed concurrently").WithCause(err)
			}
			return err
		}
		data := map[string]any{"from": from, "to": v.Status}
		if v.RejectionReason != "" {
			data["rejection_reason"] = v.RejectionReason
		}
		if err := s.Audit.Record(ctx, audit.Entry{Action: action, ObjectType: "post_version", ObjectID: v.ID.String(),
			ActorType: actor, Data: data}); err != nil {
			return err
		}
		// R-5 этапа «Заказ и размещения»: заказ и размещения узнают о смене в той же транзакции.
		if err := s.Listener.VersionChanged(ctx, domain.VersionChange{VersionID: v.ID, PostID: v.PostID, From: from, To: v.Status}); err != nil {
			return err
		}
		s.log.DebugContext(ctx, "post version listener notified", logx.KeyOp, op, KeyVersionID, v.ID, "to", v.Status)
		return nil
	})
	if err != nil {
		return domain.Version{}, err
	}
	s.log.InfoContext(ctx, "post version status changed", logx.KeyOp, op, KeyVersionID, v.ID, "from", from, "to", v.Status)
	return v, nil
}

// GetVersion — версия с публичными ссылками изображений.
func (s *Service) GetVersion(ctx context.Context, versionID uuid.UUID) (VersionView, error) {
	v, err := s.Versions.Get(ctx, versionID)
	if err != nil {
		return VersionView{}, notFound(err, "post version")
	}
	urls := make([]string, 0, len(v.Media))
	for _, m := range v.Media {
		d, err := s.Derivatives.Get(ctx, m.DerivativeID)
		if err != nil {
			return VersionView{}, fmt.Errorf("version %s derivative %s: %w", v.ID, m.DerivativeID, err)
		}
		urls = append(urls, d.PublicURL)
	}
	return VersionView{Version: v, MediaURLs: urls}, nil
}
