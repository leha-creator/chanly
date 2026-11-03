package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Carts — позиции корзины.
type Carts struct{ s *Store }

func toCartItem(r sqlc.CartItem) domain.CartItem {
	return domain.CartItem{
		ID: r.ID, OrganizationID: r.OrganizationID, ChannelID: r.ChannelID, FormatID: r.FormatID,
		ShownTotal: money.Kopecks(r.ShownTotalAmount), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (r *Carts) List(ctx context.Context, orgID uuid.UUID) ([]domain.CartItem, error) {
	rows, err := r.s.q(ctx).ListCartItems(ctx, orgID)
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Carts.List", err)
	}
	out := make([]domain.CartItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCartItem(row))
	}
	return out, nil
}

func (r *Carts) Get(ctx context.Context, orgID, id uuid.UUID) (domain.CartItem, error) {
	row, err := r.s.q(ctx).GetCartItem(ctx, sqlc.GetCartItemParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.CartItem{}, r.s.fail(ctx, "Placement.Carts.Get", err)
	}
	return toCartItem(row), nil
}

func (r *Carts) Insert(ctx context.Context, it domain.CartItem) error {
	const op = "Placement.Carts.Insert"
	err := r.s.q(ctx).InsertCartItem(ctx, sqlc.InsertCartItemParams{
		ID: it.ID, OrganizationID: it.OrganizationID, ChannelID: it.ChannelID, FormatID: it.FormatID,
		ShownTotalAmount: int64(it.ShownTotal), CreatedBy: it.CreatedBy, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	})
	if db.UniqueViolation(err, "cart_item_organization_id_channel_id_key") {
		return domain.ErrAlreadyInCart
	}
	if err != nil {
		return r.s.fail(ctx, op, err)
	}
	r.s.debug(ctx, op, "cart_item_id", it.ID, "channel_id", it.ChannelID)
	return nil
}

func (r *Carts) Update(ctx context.Context, it domain.CartItem) error {
	n, err := r.s.q(ctx).UpdateCartItem(ctx, sqlc.UpdateCartItemParams{
		ID: it.ID, OrganizationID: it.OrganizationID, FormatID: it.FormatID, ShownTotalAmount: int64(it.ShownTotal),
		UpdatedAt: it.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, "Placement.Carts.Update", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Carts) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := r.s.q(ctx).DeleteCartItem(ctx, sqlc.DeleteCartItemParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return r.s.fail(ctx, "Placement.Carts.Delete", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Carts) Clear(ctx context.Context, orgID uuid.UUID) error {
	if err := r.s.q(ctx).ClearCart(ctx, orgID); err != nil {
		return r.s.fail(ctx, "Placement.Carts.Clear", err)
	}
	return nil
}

func (r *Carts) Count(ctx context.Context, orgID uuid.UUID) (int, error) {
	n, err := r.s.q(ctx).CountCartItems(ctx, orgID)
	if err != nil {
		return 0, r.s.fail(ctx, "Placement.Carts.Count", err)
	}
	return int(n), nil
}

func (r *Carts) ChannelsInCart(ctx context.Context, orgID uuid.UUID, channelIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	ids, err := r.s.q(ctx).ChannelsInCart(ctx, sqlc.ChannelsInCartParams{OrganizationID: orgID, ChannelIds: channelIDs})
	if err != nil {
		return nil, r.s.fail(ctx, "Placement.Carts.ChannelsInCart", err)
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
