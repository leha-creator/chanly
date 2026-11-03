package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ReasonDevAccept — причина P-12 служебной команды разработчика.
const ReasonDevAccept = "dev_accept"

// DevAccept — приёмка размещения без кабинета (R-4 этапа выплат, только development и test:
// команда maxim dev placement accept). Служебная: настоящие P-12, P-13 и P-15 —
// AcceptPlacement, autoAccept и ResolveDispute; путь тот же (settle): распределение удержания по доле share (03 §7), removed → accepted, расчёт удержания
// (settlement) и создание выплаты через порт Payouts. Возвращает идентификатор выплаты или uuid.Nil,
// если выплачивать нечего.
func (s *Service) DevAccept(ctx context.Context, placementID uuid.UUID, share int) (uuid.UUID, error) {
	const op = "Placement.DevAccept"
	var payoutID uuid.UUID
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		p0, err := s.Placements.Get(ctx, placementID)
		if err != nil {
			return err
		}
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		d, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, share)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		change, err := p.Apply(domain.P12AcceptByAdvertiser, domain.ActorAdvertiser, ReasonDevAccept, s.Clock.Now())
		if err != nil {
			return s.rejected(ctx, op, p, domain.P12AcceptByAdvertiser, err)
		}
		change.CancelJobs = []string{domain.TimerKey(p.ID, domain.TimerClaim)}
		if payoutID, err = s.settle(ctx, op, o, &p, change, d, source{Key: "dev_accept:" + p.ID.String()},
			domain.Settler{Actor: domain.ActorSystem}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "placement accepted by dev command", logx.KeyOp, op, KeyPlacementID, p.ID,
			"performance_share", share, "payout_amount", int64(d.PayoutAmount), "payout_id", payoutID)
		return s.refreshOrder(ctx, op, &o)
	})
	return payoutID, err
}
