package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// JobRef — задание, выполняющее сценарий: источник перехода в истории и номер попытки.
type JobRef struct {
	ID uuid.UUID
	// Attempts — сколько раз задание уже завершалось ошибкой.
	Attempts int
	// Reason — причина снятия из полезной нагрузки remove_placement; пусто — по сроку.
	Reason domain.RemovalReason
}

// publishLookbackBefore — насколько раньше publish_at искать свой пост после неизвестного исхода.
const publishLookbackBefore = time.Minute

// Publish — задание publish_placement (S-21, R-2): предусловия в транзакции, загрузка и отправка
// вне её (I-13), публикация и P-08 — в транзакции. Повтор после успеха ничего не меняет (03 §11).
func (s *Service) Publish(ctx context.Context, placementID uuid.UUID, j JobRef) error {
	const op = "Placement.Publish"
	ctx = logx.With(ctx, KeyPlacementID, placementID.String())
	ctx = domain.WithCallPriority(ctx, domain.CallPublish)
	p0, err := s.Placements.Get(ctx, placementID)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.WarnContext(ctx, "publish job for unknown placement", logx.KeyOp, op)
		return nil
	}
	if err != nil {
		return err
	}
	if p0.Status != domain.StatusScheduled {
		s.log.DebugContext(ctx, "publish job outdated", logx.KeyOp, op, "status", p0.Status)
		return nil
	}
	content, err := s.Content.Get(ctx, p0.PostVersionID)
	if err != nil {
		return err
	}

	// Шаг 1: предусловия под блокировками (каналы → заказ → размещения).
	var (
		p             domain.Placement
		ch            domain.ChannelOffer
		done, invalid bool
	)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if ch, err = s.Channels.LockForPlacement(ctx, p0.ChannelID); err != nil {
			return err
		}
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		if p, err = s.Placements.GetForUpdate(ctx, placementID); err != nil {
			return err
		}
		if p.Status != domain.StatusScheduled {
			done = true
			s.log.DebugContext(ctx, "publish job outdated", logx.KeyOp, op, "status", p.Status)
			return nil
		}
		perr := domain.CheckPublish(&p, ch, content, s.Config.Control)
		var pe *domain.PreconditionError
		if errors.As(perr, &pe) {
			done, invalid = true, true
			return s.failPublishLocked(ctx, op, o, p, domain.ReasonPreconditionFailed, pe.Reason, j)
		}
		return perr
	})
	if err != nil || done {
		if err == nil && invalid {
			s.raisePublishFailed(ctx, op, p, domain.ReasonPreconditionFailed)
		}
		return err
	}
	now := s.Clock.Now()
	s.log.InfoContext(ctx, "placement publish started", logx.KeyOp, op, KeyChannelID, p.ChannelID, "attempt", j.Attempts+1,
		"delay", now.Sub(*p.PublishAt).String())

	// Шаг 2: внешние вызовы вне транзакции.
	msg, err := s.send(ctx, op, p, ch, content, j)
	if err != nil {
		return s.publishFailed(ctx, op, placementID, err, j)
	}
	// Шаг 3: публикация и P-08.
	return s.recordPublished(ctx, op, placementID, msg, j)
}

// send ищет свой пост после неизвестного исхода (R-5) или загружает изображения и отправляет пост.
func (s *Service) send(ctx context.Context, op string, p domain.Placement, ch domain.ChannelOffer, c domain.Content,
	j JobRef) (domain.SentMessage, error) {
	bot, err := s.Publisher.BotUserID(ctx)
	if err != nil {
		return domain.SentMessage{}, err
	}
	since := p.PublishAt.Add(-publishLookbackBefore)
	if floor := s.Clock.Now().Add(-s.Config.Control.PublishFindLookback); since.Before(floor) {
		since = floor
	}
	if j.Attempts > 0 {
		found, err := s.Publisher.FindPost(ctx, ch.MaxChatID, since, time.Time{}, bot, c.Erid)
		if err != nil {
			return domain.SentMessage{}, err
		}
		if found != nil {
			s.log.InfoContext(ctx, "publication found after unknown outcome", logx.KeyOp, op, "max_message_id", found.ID)
			return domain.SentMessage{ID: found.ID, At: found.At, ContentHash: found.ContentHash}, nil
		}
	}
	tokens := make([]string, 0, len(c.Media))
	for _, m := range c.Media {
		token, err := s.upload(ctx, ch.MaxChatID, m)
		if err != nil {
			return domain.SentMessage{}, err
		}
		tokens = append(tokens, token)
	}
	return s.Publisher.SendPost(ctx, ch.MaxChatID, domain.OutgoingPost{
		Text: c.FinalText, ImageTokens: tokens, RecoverSenderID: bot, RecoverSince: since, RecoverNeedle: c.Erid,
	})
}

func (s *Service) upload(ctx context.Context, chatID int64, m domain.MediaRef) (string, error) {
	r, err := s.Content.Open(ctx, m.DerivativeID)
	if err != nil {
		return "", err
	}
	defer r.Close()
	return s.Publisher.UploadImage(ctx, chatID, r, m.Size, m.ContentType)
}

// publishFailed — попытка не удалась (R-4): отказ 4xx — P-09 сразу; иначе повтор до T-03, затем P-09.
func (s *Service) publishFailed(ctx context.Context, op string, placementID uuid.UUID, cause error, j JobRef) error {
	p, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	now := s.Clock.Now()
	text := domain.PublishRetriesExhausted
	var ext *domain.ExternalError
	switch {
	case errors.As(cause, &ext):
		text = ext.Code
	case job.IsPermanent(cause):
		text = "rejected"
	default:
		retryAfter, fail := p.PublishRetry(now, s.Config.Control)
		if !fail {
			s.log.WarnContext(ctx, "placement publish retry", logx.KeyOp, op, "attempt", j.Attempts+1,
				"retry_after", retryAfter.String(), logx.KeyError, cause.Error())
			return job.Retry(cause, retryAfter)
		}
	}
	s.log.ErrorContext(ctx, "placement publish failed", logx.KeyOp, op, "reason", domain.ReasonPublishError, "error_code", text,
		logx.KeyError, cause.Error())
	return s.failPublish(ctx, op, placementID, domain.ReasonPublishError, text, j)
}

// ExpirePublish — задание публикации взято позже окна T-03 после простоя (07 §2): P-09 без вызова MAX.
func (s *Service) ExpirePublish(ctx context.Context, placementID uuid.UUID, j JobRef) error {
	const op = "Placement.ExpirePublish"
	ctx = logx.With(ctx, KeyPlacementID, placementID.String())
	s.log.ErrorContext(ctx, "placement publish failed", logx.KeyOp, op, "reason", domain.ReasonPublishError,
		"error_code", domain.PublishDowntime)
	return s.failPublish(ctx, op, placementID, domain.ReasonPublishError, domain.PublishDowntime, j)
}

// failPublish — P-09 в своей транзакции и оповещение ERROR.
func (s *Service) failPublish(ctx context.Context, op string, placementID uuid.UUID, reason, text string, j JobRef) error {
	p0, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	var failed bool
	var p domain.Placement
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		if p, err = s.Placements.GetForUpdate(ctx, placementID); err != nil {
			return err
		}
		if p.Status != domain.StatusScheduled {
			s.log.DebugContext(ctx, "publish job outdated", logx.KeyOp, op, "status", p.Status)
			return nil
		}
		failed = true
		return s.failPublishLocked(ctx, op, o, p, reason, text, j)
	})
	if err != nil {
		return err
	}
	if failed {
		s.raisePublishFailed(ctx, op, p, reason)
	}
	return nil
}

// failPublishLocked — P-09 в транзакции вызывающего: отмена снятия, снятие удержания, уведомления.
func (s *Service) failPublishLocked(ctx context.Context, op string, o domain.Order, p domain.Placement, reason, text string, j JobRef) error {
	change, err := p.FailPublish(reason, text, s.Clock.Now())
	if err != nil {
		return s.rejected(ctx, op, p, domain.P09PublishFailed, err)
	}
	if err := s.apply(ctx, op, o, p, change, source{JobID: &j.ID}); err != nil {
		return err
	}
	return s.refreshOrder(ctx, op, &o)
}

func (s *Service) raisePublishFailed(ctx context.Context, op string, p domain.Placement, reason string) {
	s.raise(ctx, op, alert.Alert{
		Reason: "placement_publish_failed", ObjectKey: p.ID.String(), Level: alert.LevelError,
		Summary: "Публикация размещения не состоялась",
		Fields:  map[string]any{"placement_id": p.ID, "order_id": p.OrderID, "channel_id": p.ChannelID, "reason": reason},
	})
}

// recordPublished — шаг 3 (R-2): публикация, P-08, перенос снятия, уведомления.
func (s *Service) recordPublished(ctx context.Context, op string, placementID uuid.UUID, msg domain.SentMessage, j JobRef) error {
	p0, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return err
	}
	var (
		delay     time.Duration
		published bool
	)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		if p.Status != domain.StatusScheduled {
			// Пост ушёл, а размещение уже не ждёт публикации: решать человеку.
			s.log.ErrorContext(ctx, "placement published after state change", logx.KeyOp, op, "status", p.Status,
				"max_message_id", msg.ID)
			s.raise(ctx, op, alert.Alert{Reason: "placement_published_unexpectedly", ObjectKey: p.ID.String(),
				Level: alert.LevelError, Summary: "Пост опубликован, но размещение уже не ждёт публикации",
				Fields: map[string]any{"placement_id": p.ID, "status": p.Status, "max_message_id": msg.ID}})
			return nil
		}
		now := s.Clock.Now()
		publishAt := *p.PublishAt
		change, err := p.Publish(msg, now)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P08Publish, err)
		}
		pub := domain.NewPublication(ids.New(), &p, msg, *p.PublishedAt, now)
		if err := s.Publications.Insert(ctx, pub); err != nil {
			if errors.Is(err, domain.ErrPublicationExists) {
				return fmt.Errorf("placement %s: %w", p.ID, err)
			}
			return err
		}
		if err := s.apply(ctx, op, o, p, change, source{JobID: &j.ID}); err != nil {
			return err
		}
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		delay, published = p.PublishedAt.Sub(publishAt), true
		s.log.InfoContext(ctx, "placement published", logx.KeyOp, op, KeyOrderID, o.ID, KeyChannelID, p.ChannelID,
			"publication_id", pub.ID, "max_message_id", msg.ID, "published_at", p.PublishedAt, "remove_at", p.RemoveAt,
			"delay_ms", delay.Milliseconds())
		return nil
	})
	if errors.Is(err, domain.ErrPublicationExists) {
		s.log.DebugContext(ctx, "publication already recorded", logx.KeyOp, op)
		return nil
	}
	if err == nil && published {
		s.Metrics.PublishDelay(delay)
	}
	return err
}
