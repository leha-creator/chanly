package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
)

// ListByIDs — каналы с ценами по идентификаторам, без блокировки (корзина, этап «Заказ и
// размещения»). Отсутствующих в ответе нет.
func (r *Channels) ListByIDs(ctx context.Context, ids []uuid.UUID) ([]*domain.Channel, error) {
	const op = "Channel.Channels.ListByIDs"
	if len(ids) == 0 {
		return []*domain.Channel{}, nil
	}
	rows, err := r.s.q(ctx).ListChannelsByIDs(ctx, ids)
	if err != nil {
		return nil, r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "requested", len(ids), "count", len(rows))
	out := make([]*domain.Channel, 0, len(rows))
	for _, row := range rows {
		out = append(out, channel(row))
	}
	if err := r.s.loadPrices(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}
