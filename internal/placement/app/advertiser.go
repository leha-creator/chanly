package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// CancelPlacement — рекламодатель отменяет размещение (P-06 advertiser_cancel, S-07) из
// created и awaiting_blogger; удержание снимается. Повтор с тем же ключом возвращает
// размещение без нового перехода (R-18).
func (s *Service) CancelPlacement(ctx context.Context, orgID, userID, id uuid.UUID, idemKey string) (PlacementView, error) {
	const op = "Placement.Cancel"
	p0, err := s.Placements.GetForAdvertiser(ctx, orgID, id)
	if err != nil {
		return PlacementView{}, notFound(err, "placement")
	}
	var out domain.Placement
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForAdvertiserForUpdate(ctx, orgID, id)
		if err != nil {
			return notFound(err, "placement")
		}
		out = p
		if repeated, err := s.repeated(ctx, op, p, idemKey); err != nil || repeated {
			return err
		}
		if err := s.cancelByAdvertiser(ctx, op, o, &p, userID, idemKey); err != nil {
			return err
		}
		out = p
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return PlacementView{}, err
	}
	return s.placementView(ctx, out)
}

// CancelOrder — рекламодатель отменяет заказ (S-07 шаг 3, R-17): P-06 для размещений в
// created и awaiting_blogger, остальные не меняются. Нечего отменять — 409 nothing_to_cancel.
func (s *Service) CancelOrder(ctx context.Context, orgID, userID, orderID uuid.UUID, idemKey string) (OrderView, error) {
	const op = "Order.Cancel"
	var out domain.Order
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetForUpdate(ctx, orgID, orderID)
		if err != nil {
			return notFound(err, "order")
		}
		out = o
		if idemKey != "" {
			if done, err := s.History.ExistsKeyInOrder(ctx, o.ID, idemKey); err != nil || done {
				if done {
					s.repeat(ctx, op, idemKey, KeyOrderID, o.ID)
				}
				return err
			}
		}
		ps, err := s.Placements.ListByOrderForUpdate(ctx, o.ID)
		if err != nil {
			return err
		}
		n := 0
		for _, p := range ps {
			if !domain.Allowed(domain.P06Cancel, domain.ActorAdvertiser, p.Status) {
				continue
			}
			if err := s.cancelByAdvertiser(ctx, op, o, &p, userID, idemKey); err != nil {
				return err
			}
			n++
		}
		if n == 0 {
			s.log.WarnContext(ctx, "order cancel rejected", logx.KeyOp, op, KeyOrderID, o.ID, "reason", "nothing_to_cancel",
				"status", o.Status)
			return ruleError(domain.ErrNothingToCancel)
		}
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "order cancelled by advertiser", logx.KeyOp, op, KeyOrderID, o.ID, "placements", n, "status", o.Status)
		out = o
		return nil
	})
	if err != nil {
		return OrderView{}, err
	}
	return s.orderView(ctx, out)
}

// cancelByAdvertiser — P-06 рекламодателем с историей, заданиями и журналом действий.
func (s *Service) cancelByAdvertiser(ctx context.Context, op string, o domain.Order, p *domain.Placement, userID uuid.UUID, key string) error {
	change, err := p.Cancel(domain.ActorAdvertiser, domain.ReasonAdvertiserCancel, s.Clock.Now())
	if err != nil {
		return s.rejected(ctx, op, *p, domain.P06Cancel, err)
	}
	change.UserID = &userID
	if err := s.apply(ctx, op, o, *p, change, source{UserID: &userID, Key: key}); err != nil {
		return err
	}
	return s.Audit.Record(ctx, audit.Entry{
		Action: actionPlacementCancel, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdvertiser,
		Data: map[string]any{"order_id": o.ID, "from": change.From, "reason": change.Reason},
	})
}

// repeated — действие с этим ключом уже выполнено для размещения (R-18).
func (s *Service) repeated(ctx context.Context, op string, p domain.Placement, key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	h, ok, err := s.History.FindByKey(ctx, p.ID, key)
	if err != nil || !ok {
		return false, err
	}
	s.repeat(ctx, op, key, KeyPlacementID, p.ID, "transition", h.Transition)
	return true, nil
}
