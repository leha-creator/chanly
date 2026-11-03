package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// CartView — корзина с текущими ценами и признаками (S-03).
type CartView struct {
	Lines []domain.CartLine
	// Total — сумма текущих итогов доступных позиций.
	Total money.Kopecks
}

// GetCart — корзина организации: текущий итог, смена цены, доступность. Позиции недоступных
// каналов не удаляются (R-8).
func (s *Service) GetCart(ctx context.Context, orgID uuid.UUID) (CartView, error) {
	items, err := s.Carts.List(ctx, orgID)
	if err != nil {
		return CartView{}, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ChannelID)
	}
	offers, err := s.Channels.Offers(ctx, ids)
	if err != nil {
		return CartView{}, err
	}
	lines := domain.EvaluateCart(items, offers, s.Config.FeeRateBP)
	return CartView{Lines: lines, Total: domain.CartTotal(lines)}, nil
}

// AddCartItem добавляет канал в корзину (S-03, R-8): канал из каталога, не свой, формат
// предложен, канала ещё нет в корзине, позиций меньше предела. Показанный итог — текущий.
func (s *Service) AddCartItem(ctx context.Context, orgID, userID, channelID uuid.UUID, formatID string) (CartView, error) {
	const op = "Cart.AddItem"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		offers, err := s.Channels.Offers(ctx, []uuid.UUID{channelID})
		if err != nil {
			return err
		}
		var offer *domain.ChannelOffer
		if o, ok := offers[channelID]; ok {
			offer = &o
		}
		in, err := s.Carts.ChannelsInCart(ctx, orgID, []uuid.UUID{channelID})
		if err != nil {
			return err
		}
		count, err := s.Carts.Count(ctx, orgID)
		if err != nil {
			return err
		}
		price, err := domain.CheckAdd(orgID, offer, formatID, in[channelID], count, s.Config.Cart)
		if err != nil {
			return s.cartRejected(ctx, op, channelID, err)
		}
		now := s.Clock.Now()
		item := domain.CartItem{ID: ids.New(), OrganizationID: orgID, ChannelID: channelID, FormatID: formatID,
			ShownTotal: domain.TotalAmount(price.Amount, s.Config.FeeRateBP), CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
		if err := s.Carts.Insert(ctx, item); err != nil {
			if errors.Is(err, domain.ErrAlreadyInCart) {
				return s.cartRejected(ctx, op, channelID, err)
			}
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionCartItemAdded, ObjectType: "cart_item", ObjectID: item.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"channel_id": channelID, "format_id": formatID, "shown_total_amount": int64(item.ShownTotal)},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "cart item added", logx.KeyOp, op, KeyChannelID, channelID, "format_id", formatID)
		return nil
	})
	if err != nil {
		return CartView{}, err
	}
	return s.GetCart(ctx, orgID)
}

// UpdateCartItem меняет формат позиции; тот же формат подтверждает изменившуюся цену (R-7):
// показанный итог становится текущим.
func (s *Service) UpdateCartItem(ctx context.Context, orgID, itemID uuid.UUID, formatID string) (CartView, error) {
	const op = "Cart.UpdateItem"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		item, err := s.Carts.Get(ctx, orgID, itemID)
		if err != nil {
			return notFound(err, "cart item")
		}
		offers, err := s.Channels.Offers(ctx, []uuid.UUID{item.ChannelID})
		if err != nil {
			return err
		}
		var offer *domain.ChannelOffer
		if o, ok := offers[item.ChannelID]; ok {
			offer = &o
		}
		price, err := domain.CheckFormat(offer, formatID)
		if err != nil {
			return s.cartRejected(ctx, op, item.ChannelID, err)
		}
		confirmed := formatID == item.FormatID
		prevFormat, prevTotal := item.FormatID, item.ShownTotal
		item.FormatID, item.ShownTotal, item.UpdatedAt = formatID, domain.TotalAmount(price.Amount, s.Config.FeeRateBP), s.Clock.Now()
		if err := s.Carts.Update(ctx, item); err != nil {
			return notFound(err, "cart item")
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionCartItemUpdated, ObjectType: "cart_item", ObjectID: item.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"channel_id": item.ChannelID, "price_confirmed": confirmed,
				"before": map[string]any{"format_id": prevFormat, "shown_total_amount": int64(prevTotal)},
				"after":  map[string]any{"format_id": item.FormatID, "shown_total_amount": int64(item.ShownTotal)}},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "cart item updated", logx.KeyOp, op, KeyChannelID, item.ChannelID, "format_id", formatID,
			"price_confirmed", confirmed)
		return nil
	})
	if err != nil {
		return CartView{}, err
	}
	return s.GetCart(ctx, orgID)
}

// DeleteCartItem убирает позицию организации; чужая — 404.
func (s *Service) DeleteCartItem(ctx context.Context, orgID, itemID uuid.UUID) (CartView, error) {
	const op = "Cart.DeleteItem"
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		item, err := s.Carts.Get(ctx, orgID, itemID)
		if err != nil {
			return notFound(err, "cart item")
		}
		if err := s.Carts.Delete(ctx, orgID, itemID); err != nil {
			return notFound(err, "cart item")
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionCartItemRemoved, ObjectType: "cart_item", ObjectID: itemID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"channel_id": item.ChannelID, "format_id": item.FormatID},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "cart item removed", logx.KeyOp, op, KeyChannelID, item.ChannelID)
		return nil
	})
	if err != nil {
		return CartView{}, err
	}
	return s.GetCart(ctx, orgID)
}

// InCart — какие из каналов в корзине организации (признак in_cart каталога, R-8).
func (s *Service) InCart(ctx context.Context, orgID uuid.UUID, channelIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	return s.Carts.ChannelsInCart(ctx, orgID, channelIDs)
}

// cartRejected пишет отказ корзины и переводит его в ответ.
func (s *Service) cartRejected(ctx context.Context, op string, channelID uuid.UUID, err error) error {
	reason := rejectReason(err)
	if errors.Is(err, domain.ErrChannelNotInCatalog) {
		reason = "not_in_catalog"
	}
	s.log.WarnContext(ctx, "cart item rejected", logx.KeyOp, op, KeyChannelID, channelID, "reason", reason)
	return ruleError(err)
}
