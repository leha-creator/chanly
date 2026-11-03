package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ReasonPayoutSucceeded — причина P-16 в истории.
const ReasonPayoutSucceeded = "payout_succeeded"

// SetPayoutPorts подключает модуль выплат после сборки (I-1 этапа выплат): проверку
// исполнителя при принятии (P-03) и создание выплаты при переходе в accepted.
func (s *Service) SetPayoutPorts(gate domain.ContractorGate, payouts domain.Payouts) {
	s.Contractors, s.Payouts = gate, payouts
}

// MarkPaid — P-16 (03 §4, R-5 этапа выплат): выплата прошла, размещение accepted → paid.
// Работает в транзакции вызывающего: вместе с отметкой выплаты и проводкой payout. В истории
// ключ — payout:{payout_id}. Не accepted — domain.ErrNotAccepted.
func (s *Service) MarkPaid(ctx context.Context, placementID, payoutID uuid.UUID) error {
	const op = "Placement.MarkPaid"
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
	if p.Status != domain.StatusAccepted {
		s.log.WarnContext(ctx, "placement transition rejected", logx.KeyOp, op, KeyPlacementID, p.ID,
			"transition", domain.P16Paid, "status", p.Status, "reason", "not_accepted", "payout_id", payoutID)
		return domain.ErrNotAccepted
	}
	change, err := p.Apply(domain.P16Paid, domain.ActorSystem, ReasonPayoutSucceeded, s.Clock.Now())
	if err != nil {
		return s.rejected(ctx, op, p, domain.P16Paid, err)
	}
	if err := s.apply(ctx, op, o, p, change, source{Key: "payout:" + payoutID.String()}); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "placement paid", logx.KeyOp, op, KeyPlacementID, p.ID, "payout_id", payoutID)
	return s.refreshOrder(ctx, op, &o)
}
