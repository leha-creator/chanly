package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// CreativeRejected — ЕРИР отклонил креатив версии после erid (S-28 шаг 2, этап «Маркировка»,
// R-12; порт marking/domain.CreativeListener). В транзакции модуля маркировки: размещения
// версии в awaiting_blogger, awaiting_schedule, scheduled — P-11 с актором system и причиной
// erir_rejected; версия не меняется, удержание сохраняется, задания публикации и таймеры
// отменяются. Размещения в created остаются; опубликованные возвращаются списком — решение
// принимает администратор. Повтор вызова новых записей истории не создаёт.
func (s *Service) CreativeRejected(ctx context.Context, versionID uuid.UUID) ([]uuid.UUID, error) {
	const op = "Order.CreativeRejected"
	var published []uuid.UUID
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		found, ok, err := s.Orders.FindByPlacementVersion(ctx, versionID)
		if err != nil || !ok {
			return err
		}
		// Порядок блокировок модуля: заказ → размещения (каналы для P-11 не блокируются, как в S-29).
		o, err := s.Orders.GetByIDForUpdate(ctx, found.ID)
		if err != nil {
			return err
		}
		ps, err := s.Placements.ListByOrderForUpdate(ctx, o.ID)
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		returned := 0
		for _, p := range ps {
			if p.PostVersionID != versionID {
				continue
			}
			switch {
			case p.Status.WasPublished():
				published = append(published, p.ID)
			case p.Status.BeforePublication() && p.Status != domain.StatusCreated:
				change, err := p.ReturnToCreated(domain.ActorSystem, domain.ReasonErirRejected, versionID, now, s.Config.Timing)
				if err != nil {
					return s.rejected(ctx, op, p, domain.P11ReturnToCreated, err)
				}
				if err := s.apply(ctx, op, o, p, change, source{}); err != nil {
					return err
				}
				returned++
			}
		}
		if returned == 0 && len(published) == 0 {
			return nil
		}
		if err := s.Notifier.Notify(ctx, domain.Notice{Template: domain.NoticeErirRejected, OrganizationID: o.OrganizationID,
			Role: domain.ActorAdvertiser, ObjectType: "order", ObjectID: o.ID.String(),
			Data: map[string]any{"order_number": o.Number, "version_id": versionID}}); err != nil {
			return err
		}
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "placements returned on erir rejection", logx.KeyOp, op, KeyOrderID, o.ID,
			"post_version_id", versionID, "returned", returned, "published", len(published))
		if len(published) > 0 {
			s.log.ErrorContext(ctx, "creative rejected after publication", logx.KeyOp, op, KeyOrderID, o.ID,
				"post_version_id", versionID, "placement_ids", published)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}
