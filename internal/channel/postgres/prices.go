package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Formats — справочник форматов размещения.
type Formats struct{ s *Store }

func (r *Formats) ListActive(ctx context.Context) ([]domain.Format, error) {
	const op = "Channel.Formats.ListActive"
	rows, err := r.s.q(ctx).ListActiveFormats(ctx)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "count", len(rows))
	out := make([]domain.Format, 0, len(rows))
	for _, f := range rows {
		out = append(out, domain.Format{ID: f.ID, Name: f.Name, TopHours: int(f.TopHours), DurationHours: int(f.DurationHours),
			IsActive: f.IsActive, Position: int(f.Position)})
	}
	return out, nil
}

// Prices — цены каналов.
type Prices struct{ s *Store }

// Replace заменяет набор цен: удаляет форматы вне набора и записывает переданные (R-5).
// Условие «active — с включённой ценой» проверяется при фиксации транзакции (R-3).
func (r *Prices) Replace(ctx context.Context, channelID uuid.UUID, prices []domain.Price, now time.Time) error {
	const op = "Channel.Prices.Replace"
	q := r.s.q(ctx)
	formats := make([]string, 0, len(prices))
	for _, p := range prices {
		formats = append(formats, p.FormatID)
	}
	if err := q.DeletePricesExcept(ctx, sqlc.DeletePricesExceptParams{ChannelID: channelID, FormatIds: formats}); err != nil {
		return r.s.fail(ctx, op, err)
	}
	for _, p := range prices {
		if err := q.UpsertPrice(ctx, sqlc.UpsertPriceParams{
			ID: ids.New(), ChannelID: channelID, FormatID: p.FormatID, Amount: int64(p.Amount), IsEnabled: p.IsEnabled, Now: now,
		}); err != nil {
			return r.s.fail(ctx, op, err)
		}
	}
	r.s.log.DebugContext(ctx, "channel prices saved", "op", op, "channel_id", channelID, "count", len(prices))
	return nil
}

// withPrices загружает цены одного канала.
func (s *Store) withPrices(ctx context.Context, c *domain.Channel) (*domain.Channel, error) {
	if err := s.loadPrices(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// loadPrices заполняет Channel.Prices одним запросом на все каналы.
func (s *Store) loadPrices(ctx context.Context, chans ...*domain.Channel) error {
	const op = "Channel.Prices.Load"
	if len(chans) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*domain.Channel, len(chans))
	list := make([]uuid.UUID, 0, len(chans))
	for _, c := range chans {
		c.Prices = []domain.Price{}
		byID[c.ID] = c
		list = append(list, c.ID)
	}
	rows, err := s.q(ctx).ListPricesByChannels(ctx, list)
	if err != nil {
		return s.fail(ctx, op, err)
	}
	for _, row := range rows {
		c := byID[row.ChannelID]
		c.Prices = append(c.Prices, domain.Price{
			FormatID: row.FormatID, Amount: money.Kopecks(row.Amount), IsEnabled: row.IsEnabled, FormatActive: row.FormatActive,
		})
	}
	return nil
}

// pricesByChannel — цены каналов для выдачи каталога.
func (s *Store) pricesByChannel(ctx context.Context, list []uuid.UUID) (map[uuid.UUID][]domain.Price, error) {
	out := make(map[uuid.UUID][]domain.Price, len(list))
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.q(ctx).ListPricesByChannels(ctx, list)
	if err != nil {
		return nil, s.fail(ctx, "Channel.Prices.ByChannel", err)
	}
	for _, row := range rows {
		out[row.ChannelID] = append(out[row.ChannelID], domain.Price{
			FormatID: row.FormatID, Amount: money.Kopecks(row.Amount), IsEnabled: row.IsEnabled, FormatActive: row.FormatActive,
		})
	}
	return out, nil
}
