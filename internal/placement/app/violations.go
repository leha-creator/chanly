package app

import (
	"context"
	"errors"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// reportViolation — найденное нарушение (R-10, R-11): запись нарушения, для removed_early — снятие
// публикации с причиной external и отмена задания снятия; P-14 и спор, если спора ещё нет (I-11).
// Повтор того же события или удаление, уже записанное, второй записи не создаёт.
func (s *Service) reportViolation(ctx context.Context, op string, v domain.Violation, src source) error {
	ctx = logx.With(ctx, KeyPlacementID, v.PlacementID.String())
	p0, err := s.Placements.Get(ctx, v.PlacementID)
	if err != nil {
		return err
	}
	var (
		recorded bool
		opened   *domain.Dispute
	)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, v.PlacementID)
		if err != nil {
			return err
		}
		pub, err := s.Publications.GetByPlacementForUpdate(ctx, v.PlacementID)
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		if v.Kind == domain.ViolationRemovedEarly && !pub.Live() {
			s.log.DebugContext(ctx, "repeated event ignored", logx.KeyOp, op, "kind", v.Kind, "reason", "already_removed")
			return nil
		}
		v.ID, v.CreatedAt = ids.New(), now
		if err := s.Violations.Insert(ctx, v); err != nil {
			if errors.Is(err, domain.ErrDuplicateViolation) {
				s.log.DebugContext(ctx, "repeated event ignored", logx.KeyOp, op, "kind", v.Kind, "reason", "duplicate_event")
				return nil
			}
			return err
		}
		recorded = true
		s.log.WarnContext(ctx, "placement violation detected", logx.KeyOp, op, KeyOrderID, o.ID, KeyChannelID, p.ChannelID,
			"kind", v.Kind, "source", v.Source, "occurred_at", v.OccurredAt, "inbound_event_id", v.InboundEventID)
		removeAt := pub.PublishedAt
		if p.RemoveAt != nil {
			removeAt = *p.RemoveAt
		}
		if v.Kind == domain.ViolationRemovedEarly {
			pub.MarkRemovedExternally(v.OccurredAt, now)
			if err := s.Publications.Update(ctx, pub); err != nil {
				return err
			}
			if _, err := s.Jobs.Cancel(ctx, domain.RemoveKey(p.ID)); err != nil {
				return err
			}
		}
		if !domain.Allowed(domain.P14OpenDispute, domain.ActorSystem, p.Status) {
			s.log.WarnContext(ctx, "violation recorded, dispute exists", logx.KeyOp, op, "kind", v.Kind, "status", p.Status)
			return nil
		}
		share := domain.SuggestedShare(v.Kind, pub.PublishedAt, v.OccurredAt, removeAt, s.Config.Control)
		d := domain.NewSystemDispute(ids.New(), v, share, now, s.Config.Control)
		if err := s.Disputes.Insert(ctx, d); err != nil {
			if errors.Is(err, domain.ErrDisputeExists) {
				s.log.WarnContext(ctx, "violation recorded, dispute exists", logx.KeyOp, op, "kind", v.Kind, "status", p.Status)
				return nil
			}
			return err
		}
		change, err := p.OpenDispute(v.Kind, now, s.Config.Control)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P14OpenDispute, err)
		}
		if err := s.apply(ctx, op, o, p, change, src); err != nil {
			return err
		}
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		opened = &d
		s.log.InfoContext(ctx, "placement dispute opened", logx.KeyOp, op, KeyOrderID, o.ID, "reason", d.Reason,
			"suggested_share", d.SuggestedShare, "deadline_at", d.DeadlineAt)
		return nil
	})
	if err != nil {
		return err
	}
	if recorded {
		s.Metrics.Violation(v.Kind, v.Source)
	}
	if opened != nil {
		s.disputeOpened(ctx, op, *opened, "Открыт спор по нарушению размещения")
	}
	return nil
}
