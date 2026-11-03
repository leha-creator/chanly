package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// cancelOnChannelLoss — состояния, из которых потеря канала отменяет размещение (S-25 шаг 2).
// scheduled не меняется: публикацию решит задание publish_placement (P-09), опубликованные —
// спор bot_access_lost (P-14, R-10 этапа «Автопостинг и контроль»).
var cancelOnChannelLoss = []domain.Status{domain.StatusCreated, domain.StatusAwaitingBlogger, domain.StatusAwaitingSchedule}

// ChannelUnavailable — канал стал недоступен (S-25): P-06 channel_unavailable для размещений до
// принятия и планирования. Вызывается модулем каналов в транзакции смены состояния канала,
// строка канала уже заблокирована (порядок модуля: канал → заказ → размещение).
func (s *Service) ChannelUnavailable(ctx context.Context, channelID uuid.UUID, from, to string) error {
	const op = "Placement.ChannelUnavailable"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		list, err := s.Placements.ListByChannel(ctx, channelID,
			append(append([]domain.Status{}, cancelOnChannelLoss...), domain.StatusPublished))
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		cancelled := 0
		for _, p0 := range list {
			if p0.Status == domain.StatusPublished {
				if err := s.reportViolation(ctx, op, domain.Violation{PlacementID: p0.ID, Kind: domain.ViolationBotAccessLost,
					OccurredAt: now, Source: domain.SourceEvent}, source{}); err != nil {
					return err
				}
				continue
			}
			o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
			if err != nil {
				return err
			}
			p, err := s.Placements.GetForUpdate(ctx, p0.ID)
			if err != nil {
				return err
			}
			if !domain.Allowed(domain.P06Cancel, domain.ActorSystem, p.Status) || p.Status == domain.StatusScheduled {
				continue
			}
			change, err := p.Cancel(domain.ActorSystem, domain.ReasonChannelUnavailable, now)
			if err != nil {
				return s.rejected(ctx, op, p, domain.P06Cancel, err)
			}
			if err := s.apply(ctx, op, o, p, change, source{}); err != nil {
				return err
			}
			if err := s.refreshOrder(ctx, op, &o); err != nil {
				return err
			}
			cancelled++
		}
		s.log.InfoContext(ctx, "placements cancelled on channel loss", logx.KeyOp, op, KeyChannelID, channelID,
			"from", from, "to", to, "count", cancelled)
		return nil
	})
}

// ChannelRestored — канал снова доступен: отменённые размещения не возвращаются.
func (s *Service) ChannelRestored(ctx context.Context, channelID uuid.UUID, from, to string) error {
	s.log.DebugContext(ctx, "channel restored, placements unchanged", logx.KeyOp, "Placement.ChannelRestored",
		KeyChannelID, channelID, "from", from, "to", to)
	return nil
}

// BusyIntervals — интервалы топа размещений канала для карточки каталога (R-8 этапа «Каталог»).
func (s *Service) BusyIntervals(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]domain.Interval, error) {
	return s.Placements.BusyIntervals(ctx, channelID, from, to, uuid.Nil)
}

// ScheduledConflict — интервал недоступности [from, to) пересекает срок запланированного
// размещения канала (R-8 этапа «Каталог»).
func (s *Service) ScheduledConflict(ctx context.Context, channelID uuid.UUID, from, to time.Time) (bool, error) {
	ids, err := s.Placements.ScheduledOverlapping(ctx, channelID, from, to)
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}
