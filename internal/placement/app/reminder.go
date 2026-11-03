package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Remind — задание placement_reminder (R-8 этапа уведомлений): запрос всё ещё ждёт блогера и
// срок T-01 не прошёл — напоминание блогеру; иначе задание устарело и ничего не делает.
// Повтор задания не дублирует уведомление: ключ повтора — ReminderKey.
func (s *Service) Remind(ctx context.Context, placementID uuid.UUID, _ JobRef) error {
	const op = "Placement.Remind"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Placements.Get(ctx, placementID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.WarnContext(ctx, "placement reminder for unknown placement", logx.KeyOp, op, KeyPlacementID, placementID)
			return nil
		}
		if err != nil {
			return err
		}
		n, ok := p.AcceptReminder(s.Clock.Now())
		if !ok {
			s.log.DebugContext(ctx, "placement reminder outdated", logx.KeyOp, op, KeyPlacementID, p.ID, "status", p.Status)
			return nil
		}
		o, err := s.Orders.GetByID(ctx, p.OrderID)
		if err != nil {
			return err
		}
		if err := s.Notifier.Notify(ctx, s.notice(o, p, n)); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "placement reminder sent", logx.KeyOp, op, KeyPlacementID, p.ID, KeyOrderID, o.ID)
		return nil
	})
}
