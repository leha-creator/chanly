package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// PlacementChannel — канал для модуля размещений (этап «Заказ и размещения»): состояние,
// видимость в каталоге (R-7 этапа «Каталог», D-11) и активные форматы для снимка размещения.
type PlacementChannel struct {
	Channel *domain.Channel
	// Visible — канал виден в каталоге: active, номер РКН подтверждён или не нужен, есть цена.
	Visible bool
	// Formats — активные форматы справочника по идентификатору.
	Formats map[string]domain.Format
}

// ChannelsForPlacement — каналы с ценами без блокировки (корзина). Отсутствующих в ответе нет.
func (s *Service) ChannelsForPlacement(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]PlacementChannel, error) {
	list, err := s.Channels.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	formats, err := s.activeFormats(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]PlacementChannel, len(list))
	for _, c := range list {
		out[c.ID] = s.placementChannel(c, formats)
	}
	return out, nil
}

// LockChannelForPlacement — канал с блокировкой строки до конца транзакции вызывающего:
// оформление, принятие и планирование (R-14, I-06, I-08). Нет канала — domain.ErrNotFound.
func (s *Service) LockChannelForPlacement(ctx context.Context, id uuid.UUID) (PlacementChannel, error) {
	c, err := s.Channels.GetForUpdate(ctx, id)
	if err != nil {
		return PlacementChannel{}, err
	}
	formats, err := s.activeFormats(ctx)
	if err != nil {
		return PlacementChannel{}, err
	}
	pc := s.placementChannel(c, formats)
	s.log.DebugContext(ctx, "channel locked for placement", logx.KeyOp, "Channel.LockForPlacement", KeyChannelID, id,
		"status", c.Status, "visible", pc.Visible)
	return pc, nil
}

// UnavailabilityBetween — интервалы недоступности канала, пересекающие [from, to) (R-13).
func (s *Service) UnavailabilityBetween(ctx context.Context, channelID uuid.UUID, from, to time.Time) ([]domain.Unavailability, error) {
	live, err := s.Unavail.ListLive(ctx, channelID, from)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Unavailability, 0, len(live))
	for _, u := range live {
		if u.StartsAt.Before(to) {
			out = append(out, u)
		}
	}
	return out, nil
}

// InCart — признак in_cart каталога для каналов страницы; без организации — пусто.
func (s *Service) InCart(ctx context.Context, orgID *uuid.UUID, ids []uuid.UUID) map[uuid.UUID]bool {
	if orgID == nil || len(ids) == 0 {
		return map[uuid.UUID]bool{}
	}
	in, err := s.Cart.InCart(ctx, *orgID, ids)
	if err != nil {
		// Признак вспомогательный: каталог отвечает и без него.
		s.log.WarnContext(ctx, "cart lookup failed", logx.KeyOp, "Catalog.InCart", logx.KeyError, err.Error())
		return map[uuid.UUID]bool{}
	}
	return in
}

func (s *Service) activeFormats(ctx context.Context) (map[string]domain.Format, error) {
	list, err := s.Deps.Formats.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.Format, len(list))
	for _, f := range list {
		out[f.ID] = f
	}
	return out, nil
}

func (s *Service) placementChannel(c *domain.Channel, formats map[string]domain.Format) PlacementChannel {
	return PlacementChannel{Channel: c, Visible: domain.CatalogVisible(c, s.Config.RknThreshold), Formats: formats}
}

// IsNotFound — канала нет (для переходника модуля размещений).
func IsNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }

// LowerRating — санкция по решению спора (A-08, этап «Приёмка, споры и возвраты»): рейтинг канала
// уменьшается на points, не ниже 0, в транзакции вызывающего. Нет канала — domain.ErrNotFound.
func (s *Service) LowerRating(ctx context.Context, channelID uuid.UUID, points int) (int, int, error) {
	from, to, err := s.Channels.LowerRating(ctx, channelID, points)
	if err != nil {
		return 0, 0, err
	}
	s.log.InfoContext(ctx, "channel rating lowered", logx.KeyOp, "Channel.LowerRating", KeyChannelID, channelID,
		"from", from, "to", to, "points", points)
	return from, to, nil
}
