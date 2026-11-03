package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// removable — состояния, в которых снятие удаляет пост (03 §4, особые правила): по сроку — published
// и disputed; при возврате (refund) — ещё refunded (R-10 этапа приёмки).
func removable(st domain.Status, reason domain.RemovalReason) bool {
	if st == domain.StatusPublished || st == domain.StatusDisputed {
		return true
	}
	return st == domain.StatusRefunded && reason == domain.RemovalRefund
}

// removalReason — причина снятия задания: пусто — по сроку.
func removalReason(j JobRef) domain.RemovalReason {
	if j.Reason == "" {
		return domain.RemovalSchedule
	}
	return j.Reason
}

// Remove — задание remove_placement (S-22, R-7): просмотры, намерение, удаление, P-10. Вызовы MAX —
// вне транзакций (I-13). Своё удаление записано заранее, поэтому message_removed не считается
// нарушением (05 §2.2). Повтор после успеха ничего не меняет (03 §11).
func (s *Service) Remove(ctx context.Context, placementID uuid.UUID, j JobRef) error {
	const op = "Placement.Remove"
	ctx = logx.With(ctx, KeyPlacementID, placementID.String())
	ctx = domain.WithCallPriority(ctx, domain.CallPublish)
	p, err := s.Placements.Get(ctx, placementID)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.WarnContext(ctx, "remove job for unknown placement", logx.KeyOp, op)
		return nil
	}
	if err != nil {
		return err
	}
	pub, err := s.Publications.GetByPlacement(ctx, placementID)
	reason := removalReason(j)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && !removalPending(p, pub, reason)) {
		s.log.DebugContext(ctx, "remove job outdated", logx.KeyOp, op, "status", p.Status)
		return nil
	}
	if err != nil {
		return err
	}
	offers, err := s.Channels.Offers(ctx, []uuid.UUID{p.ChannelID})
	if err != nil {
		return err
	}
	chatID := offers[p.ChannelID].MaxChatID
	now := s.Clock.Now()
	s.log.InfoContext(ctx, "placement removal started", logx.KeyOp, op, KeyChannelID, p.ChannelID, "max_message_id", pub.MaxMessageID,
		"attempt", j.Attempts+1, "delay", now.Sub(*p.RemoveAt).String(), "removal_reason", reason)

	// Шаг 1: итоговые просмотры.
	msg, err := s.Publisher.GetMessage(ctx, pub.MaxMessageID)
	switch {
	case errors.Is(err, domain.ErrMessageNotFound) && pub.Live() && reason != domain.RemovalSchedule:
		// ПРЕДПОЛОЖЕНИЕ (Q-03): снятие при возврате или по решению администратора, а поста уже нет —
		// снятие подтверждается без нарушения: спор уже открыт или решён.
		s.log.InfoContext(ctx, "publication already gone", logx.KeyOp, op, "removal_reason", reason)
		return s.recordRemoved(ctx, op, placementID, nil, j)
	case errors.Is(err, domain.ErrMessageNotFound) && pub.Live():
		// Поста нет, а мы его не удаляли — досрочное удаление (S-26).
		return s.reportViolation(ctx, op, domain.Violation{PlacementID: p.ID, Kind: domain.ViolationRemovedEarly,
			OccurredAt: now, Source: domain.SourceReconciliation}, source{JobID: &j.ID})
	case errors.Is(err, domain.ErrMessageNotFound):
		// Намерение уже записано прошлой попыткой, пост удалён: подтверждаем снятие.
		return s.recordRemoved(ctx, op, placementID, nil, j)
	case err != nil:
		return s.removeFailed(ctx, op, placementID, err, j, false)
	}

	// Шаг 2: намерение до вызова удаления.
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		pub, err := s.Publications.GetByPlacementForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		if pub.RemovedAt == nil {
			pub.MarkRemovingFor(reason, s.Clock.Now())
			return s.Publications.Update(ctx, pub)
		}
		return nil
	}); err != nil {
		return err
	}
	// Шаг 3: удаление; успех проверяется по телу ответа (05 §1).
	if err := s.Publisher.DeleteMessage(ctx, chatID, pub.MaxMessageID); err != nil {
		return s.removeFailed(ctx, op, placementID, err, j, true)
	}
	// Шаг 4: P-10 или, в споре, только снятие публикации.
	return s.recordRemoved(ctx, op, placementID, msg.Views, j)
}

// removalPending — снятие ещё нужно: публикация в канале или намерение с той же причиной записано,
// а снятие не подтверждено (removed_at размещения пуст); состояние — removable.
func removalPending(p domain.Placement, pub domain.Publication, reason domain.RemovalReason) bool {
	if !removable(p.Status, reason) {
		return false
	}
	if pub.Live() {
		return true
	}
	return pub.RemovalReason == reason && p.RemovedAt == nil
}

// removeFailed — удаление не удалось (R-7): намерение откатывается, публикация removal_overdue,
// повтор с нарастающей задержкой до T-10, затем — без повторов, оповещение и уведомление блогеру.
func (s *Service) removeFailed(ctx context.Context, op string, placementID uuid.UUID, cause error, j JobRef, intent bool) error {
	p0, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	retryAfter, exhausted := p0.RemoveRetry(j.Attempts, s.Clock.Now(), s.Config.Control)
	var pubID uuid.UUID
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		pub, err := s.Publications.GetByPlacementForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		pubID = pub.ID
		if intent {
			pub.RollbackRemoving(s.Clock.Now())
			if err := s.Publications.Update(ctx, pub); err != nil {
				return err
			}
		}
		if exhausted {
			return s.Notifier.Notify(ctx, s.notice(o, p, domain.NoticeTo{Template: domain.NoticeRemovalOverdue, Role: domain.ActorBlogger}))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !exhausted {
		s.log.WarnContext(ctx, "placement removal retry", logx.KeyOp, op, "attempt", j.Attempts+1,
			"retry_after", retryAfter.String(), logx.KeyError, cause.Error())
		return job.Retry(cause, retryAfter)
	}
	s.log.WarnContext(ctx, "publication removal overdue", logx.KeyOp, op, "publication_id", pubID, "since", p0.RemoveAt,
		logx.KeyError, cause.Error())
	s.raise(ctx, op, alert.Alert{
		Reason: "publication_removal_overdue", ObjectKey: pubID.String(), Level: alert.LevelWarn,
		Summary: "Пост не удалось снять по сроку (T-10)",
		Fields:  map[string]any{"placement_id": placementID, "channel_id": p0.ChannelID, "remove_at": p0.RemoveAt},
	})
	return nil
}

// recordRemoved — снятие подтверждено (S-22 шаг 3): published → P-10 с итоговыми просмотрами
// и окном претензий; disputed — состояние не меняется.
func (s *Service) recordRemoved(ctx context.Context, op string, placementID uuid.UUID, views *int, j JobRef) error {
	p0, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	var removed bool
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		pub, err := s.Publications.GetByPlacementForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		if pub.RemovedAt == nil {
			pub.MarkRemovingFor(removalReason(j), now)
		}
		removedAt := *pub.RemovedAt
		pub.MarkRemoved(now)
		if views != nil {
			pub.UpdateViews(views, now)
		}
		if err := s.Publications.Update(ctx, pub); err != nil {
			return err
		}
		if p.Status != domain.StatusPublished {
			// I-4: в disputed и refunded — время снятия и просмотры без перехода.
			if p.RemovedAt == nil {
				lv := p.LockVersion
				p.RemovedAt, p.UpdatedAt, p.LockVersion = &removedAt, now, lv+1
				if views != nil {
					v := *views
					p.FinalViews = &v
				}
				if err := s.Placements.UpdateIfStatus(ctx, p, p.Status, lv); err != nil {
					return err
				}
			}
			s.log.InfoContext(ctx, "publication removed, placement state kept", logx.KeyOp, op, "status", p.Status,
				"publication_id", pub.ID, "removal_reason", pub.RemovalReason, "final_views", views)
			return nil
		}
		change, err := p.Remove(views, removedAt, s.Config.Control)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P10Remove, err)
		}
		if err := s.apply(ctx, op, o, p, change, source{JobID: &j.ID}); err != nil {
			return err
		}
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		removed = true
		s.log.InfoContext(ctx, "placement removed", logx.KeyOp, op, KeyOrderID, o.ID, KeyChannelID, p.ChannelID,
			"publication_id", pub.ID, "final_views", views, "removed_at", removedAt, "claim_deadline_at", p.ClaimDeadlineAt,
			"delay_ms", removedAt.Sub(*p.RemoveAt).Milliseconds())
		s.Metrics.RemoveDelay(removedAt.Sub(*p.RemoveAt))
		return nil
	})
	if err == nil && !removed {
		s.log.DebugContext(ctx, "remove job finished without transition", logx.KeyOp, op)
	}
	return err
}
