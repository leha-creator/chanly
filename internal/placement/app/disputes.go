package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Действия журнала споров (операции администратора, S-18, S-19).
const (
	actionDisputeResolved   = "dispute.resolved"
	actionDisputeOpened     = "dispute.opened"
	actionDisputeRemovePost = "dispute.remove_post"
)

// ResolveInput — решение администратора по спору (S-18, R-8 этапа приёмки).
type ResolveInput struct {
	PlacementID uuid.UUID
	Resolution  domain.Resolution
	// Share — доля для proportional; nil — предложение системы.
	Share   *int
	Comment string
	AdminID uuid.UUID
}

// ResolveResult — итог решения: размещение, спор, распределение, выплата и рейтинг канала.
type ResolveResult struct {
	Placement    domain.Placement
	Dispute      domain.Dispute
	Distribution domain.Distribution
	// PayoutID — созданная выплата; uuid.Nil — выплаты нет (full_refund или сумма 0).
	PayoutID uuid.UUID
	// RatingFrom и RatingTo — рейтинг канала до и после санкции; равны — без санкции.
	RatingFrom, RatingTo int
	// RemovalScheduled — живой пост будет удалён сейчас (full_refund, R-10).
	RemovalScheduled bool
}

// ResolveDispute — P-15 (S-18): решение окончательное. В одной транзакции — решение в dispute,
// распределение, переход в accepted (расчёт удержания и выплата, R-5) или refunded (снятие
// удержания и, при живой публикации, удаление поста сейчас — R-10), отмена T-06, уведомления
// обеим сторонам, санкция рейтингом (R-13), audit_log. После — закрытие оповещений по спору.
func (s *Service) ResolveDispute(ctx context.Context, in ResolveInput) (ResolveResult, error) {
	const op = "Dispute.Resolve"
	ctx = logx.With(ctx, KeyPlacementID, in.PlacementID.String())
	p0, err := s.Placements.Get(ctx, in.PlacementID)
	if err != nil {
		return ResolveResult{}, notFound(err, "placement")
	}
	var res ResolveResult
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		// I-3: канал → заказ → размещение → спор → публикация.
		if _, err := s.Channels.LockForPlacement(ctx, p0.ChannelID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, in.PlacementID)
		if err != nil {
			return err
		}
		d, err := s.Disputes.GetByPlacementForUpdate(ctx, p.ID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.WarnContext(ctx, "dispute resolve rejected", logx.KeyOp, op, "reason", "no_dispute", "status", p.Status)
			return apperr.NotFound("dispute not found")
		}
		if err != nil {
			return err
		}
		if d.Resolved() {
			return s.rejected(ctx, op, p, domain.P15ResolveDispute, domain.ErrDisputeResolved)
		}
		share, err := domain.ResolutionShare(in.Resolution, in.Share, d.SuggestedShare)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P15ResolveDispute, err)
		}
		now := s.Clock.Now()
		if err := d.Resolve(in.Resolution, share, in.Comment, in.AdminID, now); err != nil {
			return s.rejected(ctx, op, p, domain.P15ResolveDispute, err)
		}
		dist, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, share)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		before := p
		change, err := p.ResolveDispute(in.Resolution, in.AdminID, dist, now)
		if err != nil {
			return s.rejected(ctx, op, before, domain.P15ResolveDispute, err)
		}
		pub, err := s.publicationForUpdate(ctx, p.ID)
		if err != nil {
			return err
		}
		if change.To == domain.StatusAccepted {
			if err := s.checkSettleable(ctx, op, before, domain.P15ResolveDispute, pub); err != nil {
				return err
			}
		} else if pub != nil && pub.Live() {
			// R-10: возврат при живом посте — удалить сейчас с причиной refund.
			change.CancelJobs = append(change.CancelJobs, domain.RemoveKey(p.ID))
			change.Jobs = append(change.Jobs, domain.RemoveNowJob(p.ID, domain.RemovalRefund, now))
			res.RemovalScheduled = true
		}
		if err := s.Disputes.Resolve(ctx, d); err != nil {
			return s.rejected(ctx, op, before, domain.P15ResolveDispute, err)
		}
		payoutID, err := s.settle(ctx, op, o, &p, change, dist, source{UserID: &in.AdminID},
			domain.Settler{Actor: domain.ActorAdmin, UserID: &in.AdminID})
		if err != nil {
			return err
		}
		from, to, err := s.lowerRating(ctx, p, in.Resolution)
		if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionDisputeResolved, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin,
			Data: map[string]any{
				"dispute_id": d.ID, "admin_user_id": in.AdminID, "resolution": in.Resolution, "performance_share": share,
				"payout_amount": int64(dist.PayoutAmount), "fee_charged": int64(dist.FeeCharged),
				"refund_amount": int64(dist.RefundAmount), "payout_id": payoutID, "rating_from": from, "rating_to": to,
				"comment": d.ResolutionComment,
			},
		}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "dispute resolved", logx.KeyOp, op, KeyOrderID, o.ID, KeyChannelID, p.ChannelID,
			"dispute_id", d.ID, "resolution", in.Resolution, "performance_share", share, "payout_amount", int64(dist.PayoutAmount),
			"fee_charged", int64(dist.FeeCharged), "refund_amount", int64(dist.RefundAmount), "payout_id", payoutID,
			"admin_user_id", in.AdminID, "removal_scheduled", res.RemovalScheduled)
		if err := s.refreshOrder(ctx, op, &o); err != nil {
			return err
		}
		res.Placement, res.Dispute, res.Distribution, res.PayoutID = p, d, dist, payoutID
		res.RatingFrom, res.RatingTo = from, to
		return nil
	})
	if err != nil {
		return ResolveResult{}, err
	}
	s.Metrics.DisputeResolved(in.Resolution, res.Dispute.ResolvedAt.Sub(res.Dispute.CreatedAt))
	s.resolveAlert(ctx, op, "placement_disputed", in.PlacementID.String())
	s.resolveAlert(ctx, op, "dispute_overdue", in.PlacementID.String())
	return res, nil
}

// lowerRating — санкция рейтингом по решению (R-13); без порта или штрафа — без изменений.
func (s *Service) lowerRating(ctx context.Context, p domain.Placement, r domain.Resolution) (int, int, error) {
	points := domain.RatingPenalty(r, s.Config.Control)
	if points <= 0 || s.Ratings == nil {
		return 0, 0, nil
	}
	from, to, err := s.Ratings.Lower(ctx, p.ChannelID, points)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.WarnContext(ctx, "channel rating not lowered", logx.KeyOp, "Dispute.Resolve", KeyChannelID, p.ChannelID,
			"reason", "channel_not_found")
		return 0, 0, nil
	}
	return from, to, err
}

// publicationForUpdate — публикация размещения с блокировкой; нет — nil.
func (s *Service) publicationForUpdate(ctx context.Context, placementID uuid.UUID) (*domain.Publication, error) {
	pub, err := s.Publications.GetByPlacementForUpdate(ctx, placementID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pub, nil
}

// OpenDisputeByAdmin — admin_open_dispute (S-19, R-12): P-14 администратором из published или
// removed с причиной admin; основание — в истории (reason_text) и audit_log.
func (s *Service) OpenDisputeByAdmin(ctx context.Context, placementID, adminID uuid.UUID, reason string) (domain.Dispute, error) {
	const op = "Dispute.Open"
	ctx = logx.With(ctx, KeyPlacementID, placementID.String())
	if reason == "" {
		return domain.Dispute{}, ruleError(domain.ErrReasonRequired)
	}
	var d domain.Dispute
	err := s.adminTx(ctx, placementID, func(ctx context.Context, o domain.Order, p domain.Placement, _ *domain.Publication) error {
		var err error
		d, err = s.openByAdmin(ctx, op, o, p, adminID, reason)
		if err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionDisputeOpened, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"dispute_id": d.ID, "admin_user_id": adminID, "from": p.Status},
		}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "admin dispute opened", logx.KeyOp, op, "dispute_id", d.ID, "from", p.Status,
			"admin_user_id", adminID, "deadline_at", d.DeadlineAt)
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return domain.Dispute{}, err
	}
	s.disputeOpened(ctx, op, d, "Администратор открыл спор по размещению")
	return d, nil
}

// openByAdmin — P-14 администратором и запись спора в транзакции вызывающего.
func (s *Service) openByAdmin(ctx context.Context, op string, o domain.Order, p domain.Placement, adminID uuid.UUID, reason string) (domain.Dispute, error) {
	before := p
	now := s.Clock.Now()
	change, err := p.OpenDisputeByAdmin(adminID, reason, now, s.Config.Control)
	if err != nil {
		return domain.Dispute{}, s.rejected(ctx, op, before, domain.P14OpenDispute, err)
	}
	d := domain.NewAdminDispute(ids.New(), p.ID, now, s.Config.Control)
	if err := s.Disputes.Insert(ctx, d); err != nil {
		if errors.Is(err, domain.ErrDisputeExists) {
			return domain.Dispute{}, s.rejected(ctx, op, before, domain.P14OpenDispute, err)
		}
		return domain.Dispute{}, err
	}
	if err := s.apply(ctx, op, o, p, change, source{UserID: &adminID}); err != nil {
		return domain.Dispute{}, err
	}
	return d, nil
}

// RemovePostResult — итог admin_remove_post: открыт ли спор этим действием.
type RemovePostResult struct {
	Dispute domain.Dispute
	Opened  bool
}

// RemovePostByAdmin — admin_remove_post (S-19, R-11): published или disputed с живой публикацией.
// В одной транзакции — P-14 администратором (если спора нет) и снятие «сейчас» с причиной admin;
// пост удаляет задание remove_placement (I-13). Иначе — 409 publication_not_live.
func (s *Service) RemovePostByAdmin(ctx context.Context, placementID, adminID uuid.UUID, reason string) (RemovePostResult, error) {
	const op = "Dispute.RemovePost"
	ctx = logx.With(ctx, KeyPlacementID, placementID.String())
	if reason == "" {
		return RemovePostResult{}, ruleError(domain.ErrReasonRequired)
	}
	var out RemovePostResult
	err := s.adminTx(ctx, placementID, func(ctx context.Context, o domain.Order, p domain.Placement, pub *domain.Publication) error {
		if pub == nil || !pub.Live() || (p.Status != domain.StatusPublished && p.Status != domain.StatusDisputed) {
			s.log.WarnContext(ctx, "admin post removal rejected", logx.KeyOp, op, "reason", "publication_not_live",
				"status", p.Status)
			return ruleError(domain.ErrPublicationNotLive)
		}
		now := s.Clock.Now()
		if p.Status == domain.StatusPublished {
			d, err := s.openByAdmin(ctx, op, o, p, adminID, reason)
			if err != nil {
				return err
			}
			out = RemovePostResult{Dispute: d, Opened: true}
		} else {
			d, err := s.Disputes.GetByPlacement(ctx, p.ID)
			if err != nil {
				return err
			}
			out.Dispute = d
		}
		if _, err := s.Jobs.Cancel(ctx, domain.RemoveKey(p.ID)); err != nil {
			return err
		}
		if err := s.Jobs.Enqueue(ctx, toJob(domain.RemoveNowJob(p.ID, domain.RemovalAdmin, now))); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionDisputeRemovePost, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"publication_id": pub.ID, "admin_user_id": adminID,
				"dispute_opened": out.Opened},
		}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "admin post removal requested", logx.KeyOp, op, "publication_id", pub.ID,
			"admin_user_id", adminID, "dispute_opened", out.Opened)
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return RemovePostResult{}, err
	}
	if out.Opened {
		s.disputeOpened(ctx, op, out.Dispute, "Администратор удаляет пост и открыл спор")
	}
	return out, nil
}

// adminTx — транзакция действия администратора по размещению: заказ → размещение → публикация.
func (s *Service) adminTx(ctx context.Context, placementID uuid.UUID,
	fn func(ctx context.Context, o domain.Order, p domain.Placement, pub *domain.Publication) error) error {
	p0, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return notFound(err, "placement")
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		pub, err := s.publicationForUpdate(ctx, placementID)
		if err != nil {
			return err
		}
		return fn(ctx, o, p, pub)
	})
}

// AdminDisputeRow — строка списка споров администратора.
type AdminDisputeRow struct {
	Dispute   domain.Dispute
	Placement domain.Placement
}

// AdminDisputeFilter — фильтр списка споров: по умолчанию — открытые.
type AdminDisputeFilter struct {
	All     bool
	Overdue bool
	Limit   int
}

// AdminDisputes — споры для администратора: открытые (или все), ближайший срок первым.
func (s *Service) AdminDisputes(ctx context.Context, f AdminDisputeFilter) ([]AdminDisputeRow, error) {
	df := domain.DisputeFilter{All: f.All, Limit: f.Limit}
	if f.Overdue {
		now := s.Clock.Now()
		df.OverdueAt = &now
	}
	list, err := s.Disputes.ListAdmin(ctx, df)
	if err != nil {
		return nil, err
	}
	out := make([]AdminDisputeRow, 0, len(list))
	for _, d := range list {
		p, err := s.Placements.Get(ctx, d.PlacementID)
		if err != nil {
			return nil, err
		}
		out = append(out, AdminDisputeRow{Dispute: d, Placement: p})
	}
	return out, nil
}

// AdminDisputeView — спор целиком для администратора: претензия (с текстом), нарушения,
// публикация, история и предпросмотр распределения по предложенной доле.
type AdminDisputeView struct {
	Placement   domain.Placement
	Dispute     domain.Dispute
	Claim       *domain.Claim
	Violations  []domain.Violation
	Publication *domain.Publication
	History     []domain.HistoryEntry
	// Preview — распределение по предложенной доле; nil — предложения нет.
	Preview *domain.Distribution
	// Overdue — срок T-06 прошёл, а решения нет.
	Overdue bool
}

// AdminDispute — спор размещения для администратора.
func (s *Service) AdminDispute(ctx context.Context, placementID uuid.UUID) (AdminDisputeView, error) {
	p, err := s.Placements.Get(ctx, placementID)
	if err != nil {
		return AdminDisputeView{}, notFound(err, "placement")
	}
	d, err := s.Disputes.GetByPlacement(ctx, placementID)
	if err != nil {
		return AdminDisputeView{}, notFound(err, "dispute")
	}
	v := AdminDisputeView{Placement: p, Dispute: d, Overdue: DisputeOverdue(d, s.Clock.Now())}
	if c, err := s.Claims.GetByPlacement(ctx, placementID); err == nil {
		v.Claim = &c
	} else if !errors.Is(err, domain.ErrNotFound) {
		return AdminDisputeView{}, err
	}
	if v.Violations, err = s.Violations.ListByPlacement(ctx, placementID); err != nil {
		return AdminDisputeView{}, err
	}
	if pub, err := s.Publications.GetByPlacement(ctx, placementID); err == nil {
		v.Publication = &pub
	} else if !errors.Is(err, domain.ErrNotFound) {
		return AdminDisputeView{}, err
	}
	h, err := s.History.ListByPlacements(ctx, []uuid.UUID{placementID})
	if err != nil {
		return AdminDisputeView{}, err
	}
	v.History = h[placementID]
	if d.SuggestedShare != nil {
		if dist, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, *d.SuggestedShare); err == nil {
			v.Preview = &dist
		}
	}
	return v, nil
}

// DisputeOverdue — срок решения прошёл (для вывода команды).
func DisputeOverdue(d domain.Dispute, now time.Time) bool {
	return !d.Resolved() && !now.Before(d.DeadlineAt)
}
