package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// actionPlacementAcceptWork — действие журнала P-12 (приёмка работы рекламодателем).
const actionPlacementAcceptWork = "placement.accept_work"

// AcceptPlacement — рекламодатель принимает работу (P-12, S-08): removed → accepted с полной
// долей, расчёт удержания и выплата блогеру в той же транзакции (I-10, R-5 этапа приёмки).
// Повтор с тем же ключом возвращает размещение без нового перехода (R-4).
func (s *Service) AcceptPlacement(ctx context.Context, orgID, userID, id uuid.UUID, idemKey string) (PlacementView, error) {
	const op = "Placement.Accept"
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
		before := p
		change, err := p.AcceptWork(domain.P12AcceptByAdvertiser, domain.ActorAdvertiser, &userID, s.Clock.Now())
		if err != nil {
			return s.rejected(ctx, op, p, domain.P12AcceptByAdvertiser, err)
		}
		pub, err := s.publicationForUpdate(ctx, p.ID)
		if err != nil {
			return err
		}
		if err := s.checkSettleable(ctx, op, before, domain.P12AcceptByAdvertiser, pub); err != nil {
			return err
		}
		d, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, 10000)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		payoutID, err := s.settle(ctx, op, o, &p, change, d, source{UserID: &userID, Key: idemKey},
			domain.Settler{Actor: domain.ActorAdvertiser, UserID: &userID})
		if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionPlacementAcceptWork, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"order_id": o.ID, "payout_amount": int64(d.PayoutAmount), "payout_id": payoutID},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "placement accepted by advertiser", logx.KeyOp, op, KeyPlacementID, p.ID, KeyOrderID, o.ID,
			"payout_amount", int64(d.PayoutAmount), "payout_id", payoutID)
		out = p
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return PlacementView{}, err
	}
	return s.placementView(ctx, out)
}

// autoAccept — P-13 по T-05 (S-24): окно претензий истекло, размещение принимается с полной
// долей. Не в removed — таймер устарел (03 §11).
func (s *Service) autoAccept(ctx context.Context, op string, p0 domain.Placement, jobID uuid.UUID) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, p0.ID)
		if err != nil {
			return err
		}
		if p.Status != domain.StatusRemoved {
			s.log.DebugContext(ctx, "placement timer outdated", logx.KeyOp, op, KeyPlacementID, p.ID, "timer", domain.TimerClaim,
				"status", p.Status)
			return nil
		}
		before := p
		change, err := p.AcceptWork(domain.P13AutoAccept, domain.ActorSystem, nil, s.Clock.Now())
		if err != nil {
			return s.rejected(ctx, op, p, domain.P13AutoAccept, err)
		}
		pub, err := s.publicationForUpdate(ctx, p.ID)
		if err != nil {
			return err
		}
		if err := s.checkSettleable(ctx, op, before, domain.P13AutoAccept, pub); err != nil {
			return err
		}
		d, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, 10000)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		payoutID, err := s.settle(ctx, op, o, &p, change, d, source{JobID: &jobID}, domain.Settler{Actor: domain.ActorSystem})
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "placement auto accepted", logx.KeyOp, op, KeyPlacementID, p.ID, KeyOrderID, o.ID,
			"payout_amount", int64(d.PayoutAmount), "payout_id", payoutID)
		return s.refreshOrder(ctx, op, &o)
	})
}

// checkSettleable — I-02: публикация размещения снята. Нарушение — ERROR и отказ 409
// invalid_transition с причиной publication_live.
func (s *Service) checkSettleable(ctx context.Context, op string, p domain.Placement, code domain.TransitionCode,
	pub *domain.Publication) error {
	if err := p.CheckSettleable(code, pub); err != nil {
		if code == domain.P15ResolveDispute {
			// В споре пост может быть ещё в канале: это ожидаемый отказ администратору (R-9), не
			// нарушение инварианта — только WARN отклонённого перехода.
			return s.rejected(ctx, op, p, code, err)
		}
		s.log.ErrorContext(ctx, "placement invariant violated", logx.KeyOp, op, KeyPlacementID, p.ID, "transition", code,
			"invariant", "I-02", "status", p.Status)
		return s.rejected(ctx, op, p, code, err)
	}
	return nil
}

// settle — общая часть перехода в accepted (P-12, P-13, P-15, R-5 этапа приёмки): распределение
// до смены состояния (placement_distribution_sum), переход, расчёт удержания и выплата — в одной
// транзакции. В refunded (full_refund) расчёта и выплаты нет: удержание снимает ReleaseHold.
// Возвращает идентификатор выплаты или uuid.Nil.
func (s *Service) settle(ctx context.Context, op string, o domain.Order, p *domain.Placement, change domain.StateChange,
	d domain.Distribution, src source, by domain.Settler) (uuid.UUID, error) {
	p.SetDistribution(d)
	if err := s.Placements.SetDistribution(ctx, p.ID, d); err != nil {
		return uuid.Nil, err
	}
	if err := s.apply(ctx, op, o, *p, change, src); err != nil {
		return uuid.Nil, err
	}
	if change.To != domain.StatusAccepted {
		return uuid.Nil, nil
	}
	if err := s.Holds.Settle(ctx, p.ID, p.BloggerOrgID, d, by); err != nil {
		return uuid.Nil, err
	}
	var payoutID uuid.UUID
	if d.PayoutAmount > 0 && s.Payouts != nil {
		var err error
		if payoutID, err = s.Payouts.Create(ctx, domain.PayoutRequest{PlacementID: p.ID, BloggerOrgID: p.BloggerOrgID,
			Amount: d.PayoutAmount}); err != nil {
			return uuid.Nil, err
		}
	}
	s.Metrics.Accepted(change.Code)
	return payoutID, nil
}
