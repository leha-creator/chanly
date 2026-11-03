package app

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Checkout оформляет заказ из корзины (S-06, P-01) в одной транзакции (I-10): кампания под
// блокировкой и её полнота (R-6), каналы под блокировкой по порядку channel_id и сверка
// корзины (R-7), номер заказа (R-21), версия поста 1, заказ и размещения в created с таймером
// окна (R-11), удержания (R-2), очистка корзины, кампания в ordered (R-1). Повтор с тем же
// ключом возвращает прежний заказ (R-18). Любой отказ откатывает всё.
func (s *Service) Checkout(ctx context.Context, orgID, userID, campaignID uuid.UUID, idemKey string) (domain.Order, error) {
	const op = "Order.Checkout"
	var out domain.Order
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if idemKey != "" {
			if o, ok, err := s.Orders.FindByIdempotencyKey(ctx, orgID, idemKey); err != nil {
				return err
			} else if ok {
				s.repeat(ctx, op, idemKey, KeyOrderID, o.ID)
				out = o
				return nil
			}
		}
		c, err := s.Campaigns.LockForCheckout(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		now := s.Clock.Now()
		w, err := domain.CheckCampaign(c, now, s.Config.Timing)
		if err != nil {
			return s.checkoutRejected(ctx, op, campaignID, err)
		}
		lines, offers, err := s.lockCart(ctx, orgID)
		if err != nil {
			return err
		}
		if err := domain.CheckCartForCheckout(lines); err != nil {
			return s.checkoutRejected(ctx, op, campaignID, err)
		}
		number, err := s.Orders.NextNumber(ctx)
		if err != nil {
			return err
		}
		v, err := s.Versions.Create(ctx, orgID, campaignID, userID, number)
		if err != nil {
			var pe *domain.PostNotReadyError
			if errors.As(err, &pe) {
				return s.checkoutRejected(ctx, op, campaignID, &domain.IncompleteError{PostProblems: pe.Problems})
			}
			return err
		}
		order := domain.Order{
			ID: ids.New(), Number: number, OrganizationID: orgID, CampaignID: campaignID, PostID: c.PostID,
			CurrentPostVersionID: v.ID, Status: domain.DeriveOrderStatus(nil, v.Status), IdempotencyKey: idemKey,
			CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
		}
		placements := make([]domain.Placement, 0, len(lines))
		changes := make([]domain.StateChange, 0, len(lines))
		for _, l := range lines {
			offer := offers[l.Item.ChannelID]
			price, _ := offer.Price(l.Item.FormatID)
			p, change, err := domain.NewPlacement(ids.New(), order.ID, offer, price, w, v.ID, s.Config.FeeRateBP, userID, now, s.Config.Timing)
			if err != nil {
				return err
			}
			placements, changes = append(placements, p), append(changes, change)
		}
		order.TotalAmount = domain.OrderTotal(placements)
		if err := s.Orders.Insert(ctx, order); err != nil {
			return err
		}
		holds := make([]domain.HoldRequest, 0, len(placements))
		for i, p := range placements {
			if err := s.Placements.Insert(ctx, p); err != nil {
				return err
			}
			if err := s.record(ctx, op, order, p, changes[i], source{UserID: &userID}); err != nil {
				return err
			}
			holds = append(holds, domain.HoldRequest{PlacementID: p.ID, PostVersionID: v.ID, Amount: p.TotalAmount})
		}
		if err := s.Holds.HoldAll(ctx, orgID, holds); err != nil {
			return s.checkoutRejected(ctx, op, campaignID, err)
		}
		if err := s.Carts.Clear(ctx, orgID); err != nil {
			return err
		}
		if err := s.Campaigns.MarkOrdered(ctx, orgID, campaignID, order.ID); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionOrderCreated, ObjectType: "order", ObjectID: order.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"order_number": order.Number, "campaign_id": campaignID, "placements": len(placements),
				"total_amount": int64(order.TotalAmount), "post_version_id": v.ID},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "order created", logx.KeyOp, op, KeyOrderID, order.ID, "order_number", order.Number,
			"placements", len(placements), "total_amount", int64(order.TotalAmount))
		out = order
		return nil
	})
	return out, err
}

// lockCart — позиции корзины с каналами под блокировкой в порядке channel_id (R-7, R-14):
// одновременные оформления и планирования не блокируют друг друга навстречу.
func (s *Service) lockCart(ctx context.Context, orgID uuid.UUID) ([]domain.CartLine, map[uuid.UUID]domain.ChannelOffer, error) {
	items, err := s.Carts.List(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	channels := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		channels = append(channels, it.ChannelID)
	}
	slices.SortFunc(channels, func(a, b uuid.UUID) int { return compareUUID(a, b) })
	offers := make(map[uuid.UUID]domain.ChannelOffer, len(channels))
	for _, id := range slices.Compact(channels) {
		o, err := s.Channels.LockForPlacement(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			continue // канала нет: позиция недоступна
		}
		if err != nil {
			return nil, nil, err
		}
		offers[id] = o
	}
	return domain.EvaluateCart(items, offers, s.Config.FeeRateBP), offers, nil
}

// checkoutRejected пишет отказ оформления с причиной и подробностями без персональных данных.
func (s *Service) checkoutRejected(ctx context.Context, op string, campaignID uuid.UUID, err error) error {
	args := []any{logx.KeyOp, op, KeyCampaignID, campaignID, "reason", rejectReason(err)}
	var (
		ie *domain.IncompleteError
		cc *domain.CartChangedError
		ib *domain.InsufficientBalanceError
	)
	switch {
	case errors.As(err, &ie):
		args = append(args, "missing_fields", ie.MissingFields, "post_problems", ie.PostProblems)
	case errors.As(err, &cc):
		args = append(args, "items", cc.Items)
	case errors.As(err, &ib):
		args = append(args, "missing_amount", int64(ib.Missing))
	}
	s.log.WarnContext(ctx, "checkout rejected", args...)
	return ruleError(err)
}

// compareUUID — порядок идентификаторов как в PostgreSQL (побайтно).
func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
