package app

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// VersionChanged — последствия смены состояния версии поста (R-5): approved — заказ в
// marking; ready — P-02 (или P-06 по R-10) для размещений в created с этой версией; rejected —
// удержания размещений в created снимаются. Вызывается модулем post в транзакции перехода
// версии. Версия, которая не текущая в заказе, игнорируется.
func (s *Service) VersionChanged(ctx context.Context, versionID uuid.UUID, to domain.VersionStatus) error {
	const op = "Order.VersionChanged"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		found, ok, err := s.Orders.FindByCurrentVersion(ctx, versionID)
		if err != nil {
			return err
		}
		if !ok {
			s.log.DebugContext(ctx, "version change ignored", logx.KeyOp, op, "version_id", versionID, "reason", versionIgnoredNotCurrent)
			return nil
		}
		// Порядок блокировок модуля: каналы → заказ → размещения.
		var offers map[uuid.UUID]domain.ChannelOffer
		if to == domain.VersionReady {
			if offers, err = s.lockOrderChannels(ctx, found.ID, versionID); err != nil {
				return err
			}
		}
		o, err := s.Orders.GetByIDForUpdate(ctx, found.ID)
		if err != nil {
			return err
		}
		if o.CurrentPostVersionID != versionID {
			s.log.DebugContext(ctx, "version change ignored", logx.KeyOp, op, "version_id", versionID, "reason", versionIgnoredNotCurrent)
			return nil
		}
		ps, err := s.Placements.ListByOrderForUpdate(ctx, o.ID)
		if err != nil {
			return err
		}
		var held map[uuid.UUID]bool
		if to == domain.VersionReady {
			if held, err = s.activeHolds(ctx, ps, versionID); err != nil {
				return err
			}
		}
		now := s.Clock.Now()
		sent, cancelled, released := 0, 0, 0
		for _, p := range ps {
			if p.Status != domain.StatusCreated || p.PostVersionID != versionID {
				continue
			}
			switch to {
			case domain.VersionReady:
				offer, ok := offers[p.ChannelID]
				change, err := p.SendToBlogger(ok && offer.Active, held[p.ID], to, now, s.Config.Timing)
				if errors.Is(err, domain.ErrNoActiveHold) {
					// R-7: нарушение I-03 — ошибка в коде; размещение остаётся в created.
					s.log.ErrorContext(ctx, "placement without active hold", logx.KeyOp, op, KeyPlacementID, p.ID,
						KeyOrderID, o.ID, "transition", domain.P02SendToBlogger)
					continue
				}
				if err != nil {
					return s.rejected(ctx, op, p, domain.P02SendToBlogger, err)
				}
				if err := s.apply(ctx, op, o, p, change, source{}); err != nil {
					return err
				}
				if change.Code == domain.P02SendToBlogger {
					sent++
				} else {
					cancelled++
				}
			case domain.VersionRejected:
				// 03 §4, особые правила: размещения остаются в created, удержания снимаются.
				if err := s.Holds.Release(ctx, p.ID, holdReleaseOnRejection); err != nil {
					return err
				}
				released++
			}
		}
		if to == domain.VersionApproved {
			// Одна версия — одно уведомление: повтор события не дублирует его.
			if err := s.Notifier.Notify(ctx, domain.Notice{Template: domain.NoticePostApproved, OrganizationID: o.OrganizationID,
				Role: domain.ActorAdvertiser, ObjectType: "order", ObjectID: o.ID.String(), Key: "post_approved:" + versionID.String(),
				Data: map[string]any{"order_id": o.ID, "order_number": o.Number, "version_id": versionID}}); err != nil {
				return err
			}
		}
		if to == domain.VersionRejected {
			if err := s.Notifier.Notify(ctx, domain.Notice{Template: domain.NoticePostRejected, OrganizationID: o.OrganizationID,
				Role: domain.ActorAdvertiser, ObjectType: "order", ObjectID: o.ID.String(),
				Data: map[string]any{"order_number": o.Number, "version_id": versionID}}); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "holds released on rejection", logx.KeyOp, op, KeyOrderID, o.ID, "count", released)
		}
		if err := s.refreshOrderWith(ctx, op, &o, to, false); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "post version change applied", logx.KeyOp, op, KeyOrderID, o.ID, "version_id", versionID,
			"to", to, "placements_sent", sent, "placements_cancelled", cancelled)
		return nil
	})
}

// activeHolds — признак активного удержания для размещений в created с этой версией (R-7).
func (s *Service) activeHolds(ctx context.Context, ps []domain.Placement, versionID uuid.UUID) (map[uuid.UUID]bool, error) {
	ids := []uuid.UUID{}
	for _, p := range ps {
		if p.Status == domain.StatusCreated && p.PostVersionID == versionID {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	return s.Holds.HasActive(ctx, ids)
}

// lockOrderChannels блокирует каналы размещений заказа в created с этой версией в порядке
// channel_id (R-14, I-08) до блокировки заказа.
func (s *Service) lockOrderChannels(ctx context.Context, orderID, versionID uuid.UUID) (map[uuid.UUID]domain.ChannelOffer, error) {
	ps, err := s.Placements.ListByOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	channels := []uuid.UUID{}
	for _, p := range ps {
		if p.Status == domain.StatusCreated && p.PostVersionID == versionID {
			channels = append(channels, p.ChannelID)
		}
	}
	slices.SortFunc(channels, compareUUID)
	offers := make(map[uuid.UUID]domain.ChannelOffer, len(channels))
	for _, id := range slices.Compact(channels) {
		o, err := s.Channels.LockForPlacement(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		offers[id] = o
	}
	return offers, nil
}

// CreateOrderPostVersion — правка поста после оформления (S-29, R-15): новая версия из
// черновика кампании с тем же номером заказа; P-11 для отправленных блогеру размещений,
// размещения в created привязываются к новой версии; окно короче REEDIT_GUARD — P-06
// window_elapsed; удержания для created — заново (нехватка — ничего не сохраняется).
// Доступно в rejected и in_progress. Повтор с тем же ключом возвращает заказ (R-18).
func (s *Service) CreateOrderPostVersion(ctx context.Context, orgID, userID, orderID uuid.UUID, idemKey string) (OrderView, error) {
	const op = "Order.CreatePostVersion"
	var out domain.Order
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetForUpdate(ctx, orgID, orderID)
		if err != nil {
			return notFound(err, "order")
		}
		if idemKey != "" && o.VersionIdempotencyKey == idemKey {
			s.repeat(ctx, op, idemKey, KeyOrderID, o.ID)
			out = o
			return nil
		}
		if o.Status == domain.OrderModeration || o.Status == domain.OrderMarking {
			return s.versionRejected(ctx, op, o, domain.ErrPostVersionInProgress)
		}
		ps, err := s.Placements.ListByOrderForUpdate(ctx, o.ID)
		if err != nil {
			return err
		}
		var active []domain.Placement
		for _, p := range ps {
			if p.Status.BeforePublication() {
				active = append(active, p)
			}
		}
		if len(active) == 0 {
			return s.versionRejected(ctx, op, o, domain.ErrNothingToUpdate)
		}
		v, err := s.Versions.Create(ctx, orgID, o.CampaignID, userID, o.Number)
		if err != nil {
			var pe *domain.PostNotReadyError
			if errors.As(err, &pe) {
				return s.versionRejected(ctx, op, o, &domain.IncompleteError{PostProblems: pe.Problems})
			}
			return err
		}
		now := s.Clock.Now()
		returned, cancelled := 0, 0
		holds := []domain.HoldRequest{}
		for _, p := range active {
			switch {
			case p.WindowEnd.Sub(now) < s.Config.Timing.ReeditGuard:
				change, err := p.Cancel(domain.ActorSystem, domain.ReasonWindowElapsed, now)
				if err != nil {
					return s.rejected(ctx, op, p, domain.P06Cancel, err)
				}
				if err := s.apply(ctx, op, o, p, change, source{UserID: &userID, Key: idemKey}); err != nil {
					return err
				}
				cancelled++
				continue
			case p.Status == domain.StatusCreated:
				prev := p.BindVersion(v.ID, now)
				if err := s.Placements.UpdateIfStatus(ctx, p, domain.StatusCreated, prev); err != nil {
					return err
				}
			default:
				change, err := p.ReturnToCreated(domain.ActorAdvertiser, domain.ReasonNewPostVersion, v.ID, now, s.Config.Timing)
				if err != nil {
					return s.rejected(ctx, op, p, domain.P11ReturnToCreated, err)
				}
				if err := s.apply(ctx, op, o, p, change, source{UserID: &userID, Key: idemKey}); err != nil {
					return err
				}
				returned++
			}
			holds = append(holds, domain.HoldRequest{PlacementID: p.ID, PostVersionID: v.ID, Amount: p.TotalAmount})
		}
		// 03 §4: удержания создаются заново одной транзакцией; размещения с активным удержанием
		// (вернувшиеся по P-11) порт пропускает.
		if err := s.Holds.HoldAll(ctx, orgID, holds); err != nil {
			return s.versionRejected(ctx, op, o, err)
		}
		o.CurrentPostVersionID, o.VersionIdempotencyKey = v.ID, idemKey
		if err := s.refreshOrderWith(ctx, op, &o, v.Status, true); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionOrderPostVersion, ObjectType: "order", ObjectID: o.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"version_id": v.ID, "number": v.Number, "returned": returned, "cancelled": cancelled},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "order post version created", logx.KeyOp, op, KeyOrderID, o.ID, "version_id", v.ID,
			"number", v.Number, "returned", returned, "cancelled", cancelled)
		out = o
		return nil
	})
	if err != nil {
		return OrderView{}, err
	}
	return s.orderView(ctx, out)
}

// versionRejected пишет отказ правки поста и переводит его в ответ.
func (s *Service) versionRejected(ctx context.Context, op string, o domain.Order, err error) error {
	args := []any{logx.KeyOp, op, KeyOrderID, o.ID, "reason", rejectReason(err), "status", o.Status}
	var ib *domain.InsufficientBalanceError
	if errors.As(err, &ib) {
		args = append(args, "missing_amount", int64(ib.Missing))
	}
	s.log.WarnContext(ctx, "post version update rejected", args...)
	return ruleError(err)
}
