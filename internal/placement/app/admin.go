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
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Операции администратора над заказами и размещениями (этап «API администратора», R-4 … R-6).

// Действия журнала администратора по размещению.
const (
	actionAdminCancel      = "placement.admin_cancel"
	actionAdminMarkRemoved = "placement.admin_mark_removed"
)

// UrgentLead — до начала периода публикации меньше этого — заказ срочный (A-40).
const UrgentLead = 6 * time.Hour

// ErrAdminReadsMissing — модуль собран без выборок администратора: ошибка сборки.
var ErrAdminReadsMissing = errors.New("placement: admin reads are not wired")

// AdminOrderQuery — очередь модерации: moderation (по умолчанию), marking или all.
type AdminOrderQuery struct {
	Status        string
	Query         string
	Offset, Limit int
}

// AdminOrderItem — строка очереди с признаком срочности.
type AdminOrderItem struct {
	domain.AdminOrderRow
	Urgent bool
}

// AdminOrderView — заказ для модерации: заказ с версией и размещениями, срок и ожидание.
type AdminOrderView struct {
	OrderView
	PublishFrom  *time.Time
	WaitingSince time.Time
	Urgent       bool
}

func (s *Service) adminReads() (domain.AdminReads, error) {
	if s.Admin == nil {
		return nil, ErrAdminReadsMissing
	}
	return s.Admin, nil
}

// urgent — до начала периода меньше UrgentLead или он уже начался (A-40).
func (s *Service) urgent(status domain.OrderStatus, from *time.Time) bool {
	return status == domain.OrderModeration && from != nil && from.Sub(s.Clock.Now()) < UrgentLead
}

// AdminOrders — очередь модерации поста (S-17 шаг 1).
func (s *Service) AdminOrders(ctx context.Context, q AdminOrderQuery) ([]AdminOrderItem, int, error) {
	reads, err := s.adminReads()
	if err != nil {
		return nil, 0, err
	}
	f := domain.AdminOrderFilter{Query: q.Query, Offset: q.Offset, Limit: q.Limit}
	switch q.Status {
	case "", "moderation":
		st := domain.OrderModeration
		f.Status, f.ByDeadline = &st, true
	case "marking":
		st := domain.OrderMarking
		f.Status = &st
	case "all":
	default:
		return nil, 0, apperr.Validation("status", "unknown")
	}
	rows, total, err := reads.Orders(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	out := make([]AdminOrderItem, 0, len(rows))
	for _, r := range rows {
		from := r.PublishFrom
		out = append(out, AdminOrderItem{AdminOrderRow: r, Urgent: s.urgent(r.Status, &from)})
	}
	return out, total, nil
}

// AdminOrder — заказ для модерации (S-17).
func (s *Service) AdminOrder(ctx context.Context, id uuid.UUID) (AdminOrderView, error) {
	o, err := s.Orders.GetByID(ctx, id)
	if err != nil {
		return AdminOrderView{}, notFound(err, "order")
	}
	v, err := s.orderView(ctx, o)
	if err != nil {
		return AdminOrderView{}, err
	}
	out := AdminOrderView{OrderView: v, WaitingSince: o.UpdatedAt}
	if o.Status == domain.OrderModeration {
		out.WaitingSince = v.Version.CreatedAt
	}
	for _, p := range v.Placements {
		if out.PublishFrom == nil || p.Placement.WindowStart.Before(*out.PublishFrom) {
			ws := p.Placement.WindowStart
			out.PublishFrom = &ws
		}
	}
	out.Urgent = s.urgent(o.Status, out.PublishFrom)
	return out, nil
}

// moderated — заказ в moderation, иначе 409 invalid_transition (S-17).
func (s *Service) moderated(ctx context.Context, op string, id uuid.UUID) (domain.Order, error) {
	if s.Moderation == nil {
		return domain.Order{}, fmt.Errorf("%s: post moderation is not wired", op)
	}
	o, err := s.Orders.GetByID(ctx, id)
	if err != nil {
		return domain.Order{}, notFound(err, "order")
	}
	if o.Status != domain.OrderModeration {
		s.log.WarnContext(ctx, "admin order moderation rejected", logx.KeyOp, op, KeyOrderID, o.ID, "status", o.Status)
		return domain.Order{}, apperr.InvalidTransition("post_moderation", string(o.Status))
	}
	return o, nil
}

// ApproveOrderPost — администратор одобрил пост заказа (S-17 шаг 3). Заказ уходит в marking через
// слушатель версии; при выключенной маркировке (I-2) версия сразу получает erid разработки.
func (s *Service) ApproveOrderPost(ctx context.Context, id, adminID uuid.UUID) (AdminOrderView, error) {
	const op = "Order.AdminApprovePost"
	o, err := s.moderated(ctx, op, id)
	if err != nil {
		return AdminOrderView{}, err
	}
	if err := s.Moderation.Approve(ctx, o.CurrentPostVersionID, adminID); err != nil {
		return AdminOrderView{}, err
	}
	s.log.WarnContext(ctx, "admin order post approved", logx.KeyOp, op, KeyOrderID, o.ID,
		"version_id", o.CurrentPostVersionID, "admin_user_id", adminID)
	if s.Moderation.MarkingDisabled() {
		erid := fmt.Sprintf("dev-%d", o.Number)
		if err := s.Moderation.MarkReady(ctx, o.CurrentPostVersionID, erid); err != nil {
			return AdminOrderView{}, err
		}
		s.log.WarnContext(ctx, "marking disabled, version made ready", logx.KeyOp, op, KeyOrderID, o.ID,
			"version_id", o.CurrentPostVersionID)
	}
	return s.AdminOrder(ctx, o.ID)
}

// RejectOrderPost — администратор отклонил пост с причиной (S-17 шаг 4): заказ rejected.
func (s *Service) RejectOrderPost(ctx context.Context, id, adminID uuid.UUID, reason string) (AdminOrderView, error) {
	const op = "Order.AdminRejectPost"
	if reason == "" {
		return AdminOrderView{}, ruleError(domain.ErrReasonRequired)
	}
	o, err := s.moderated(ctx, op, id)
	if err != nil {
		return AdminOrderView{}, err
	}
	if err := s.Moderation.Reject(ctx, o.CurrentPostVersionID, adminID, reason); err != nil {
		return AdminOrderView{}, err
	}
	s.log.WarnContext(ctx, "admin order post rejected", logx.KeyOp, op, KeyOrderID, o.ID,
		"version_id", o.CurrentPostVersionID, "admin_user_id", adminID)
	return s.AdminOrder(ctx, o.ID)
}

// AdminPlacements — размещения по фильтрам (R-5).
func (s *Service) AdminPlacements(ctx context.Context, f domain.AdminPlacementFilter) ([]domain.AdminPlacementRow, int, error) {
	reads, err := s.adminReads()
	if err != nil {
		return nil, 0, err
	}
	f.Now = s.Clock.Now()
	return reads.Placements(ctx, f)
}

// AdminPlacementView — размещение целиком для администратора.
type AdminPlacementView struct {
	Placement    domain.Placement
	Channel      domain.ChannelRef
	AdvertiserID uuid.UUID
	OrderNumber  int64
	History      []domain.HistoryEntry
	Publication  *domain.Publication
	Dispute      *domain.Dispute
	Claim        *domain.Claim
	Violations   []domain.Violation
	// RemovalOverdue — пост жив после remove_at или снятие по сроку не удалось.
	RemovalOverdue bool
}

// AdminPlacement — размещение для администратора; нет — 404.
func (s *Service) AdminPlacement(ctx context.Context, id uuid.UUID) (AdminPlacementView, error) {
	p, err := s.Placements.Get(ctx, id)
	if err != nil {
		return AdminPlacementView{}, notFound(err, "placement")
	}
	o, err := s.Orders.GetByID(ctx, p.OrderID)
	if err != nil {
		return AdminPlacementView{}, err
	}
	pv, err := s.placementView(ctx, p)
	if err != nil {
		return AdminPlacementView{}, err
	}
	v := AdminPlacementView{Placement: p, Channel: pv.Channel, AdvertiserID: o.OrganizationID, OrderNumber: o.Number,
		History: pv.History, Publication: pv.Publication, Dispute: pv.Dispute, Claim: pv.Claim}
	if v.Violations, err = s.Violations.ListByPlacement(ctx, id); err != nil {
		return AdminPlacementView{}, err
	}
	if pub := v.Publication; pub != nil {
		v.RemovalOverdue = pub.RemovalOverdue || (pub.Live() && p.RemoveAt != nil && p.RemoveAt.Before(s.Clock.Now()))
	}
	return v, nil
}

// CancelByAdmin — admin_cancel (R-5): P-06 администратором до публикации; удержание снимается,
// задания отменяются, обе стороны уведомлены, основание — в истории и audit_log.
func (s *Service) CancelByAdmin(ctx context.Context, id, adminID uuid.UUID, reason string) (AdminPlacementView, error) {
	const op = "Placement.AdminCancel"
	ctx = logx.With(ctx, KeyPlacementID, id.String())
	err := s.adminTx(ctx, id, func(ctx context.Context, o domain.Order, p domain.Placement, _ *domain.Publication) error {
		before := p
		change, err := p.CancelByAdmin(adminID, reason, s.Clock.Now())
		if err != nil {
			return s.rejected(ctx, op, before, domain.P06Cancel, err)
		}
		if err := s.apply(ctx, op, o, p, change, source{UserID: &adminID}); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionAdminCancel, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"order_id": o.ID, "from": change.From, "admin_user_id": adminID},
		}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "admin placement cancelled", logx.KeyOp, op, KeyOrderID, o.ID, "from", change.From,
			"admin_user_id", adminID)
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return AdminPlacementView{}, err
	}
	return s.AdminPlacement(ctx, id)
}

// MarkRemovedByAdmin — admin_mark_removed (R-6): MAX не подтвердил снятие, администратор
// проверил канал. Публикация снята в removedAt с причиной admin, P-10 администратором, итоговые
// просмотры — последние считанные, задание снятия отменяется, T-05 — от removedAt.
func (s *Service) MarkRemovedByAdmin(ctx context.Context, id, adminID uuid.UUID, removedAt time.Time, reason string) (AdminPlacementView, error) {
	const op = "Placement.AdminMarkRemoved"
	ctx = logx.With(ctx, KeyPlacementID, id.String())
	err := s.adminTx(ctx, id, func(ctx context.Context, o domain.Order, p domain.Placement, pub *domain.Publication) error {
		if pub == nil || (!pub.Live() && !pub.RemovalOverdue) {
			s.log.WarnContext(ctx, "admin mark removed rejected", logx.KeyOp, op, "reason", "publication_not_live", "status", p.Status)
			return ruleError(domain.ErrPublicationNotLive)
		}
		now := s.Clock.Now()
		before := p
		change, err := p.MarkRemovedByAdmin(adminID, reason, pub.Views, pub.PublishedAt, removedAt, now, s.Config.Control)
		if err != nil {
			return s.rejected(ctx, op, before, domain.P10Remove, err)
		}
		pub.MarkRemovedByAdmin(removedAt, now)
		if err := s.Publications.Update(ctx, *pub); err != nil {
			return err
		}
		if err := s.apply(ctx, op, o, p, change, source{UserID: &adminID}); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionAdminMarkRemoved, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"publication_id": pub.ID, "removed_at": removedAt.UTC(),
				"final_views": p.FinalViews, "admin_user_id": adminID},
		}); err != nil {
			return err
		}
		s.log.WarnContext(ctx, "admin placement marked removed", logx.KeyOp, op, KeyOrderID, o.ID, "publication_id", pub.ID,
			"removed_at", removedAt.UTC(), "admin_user_id", adminID)
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return AdminPlacementView{}, err
	}
	s.resolveAlert(ctx, op, "publication_removal_overdue", id.String())
	return s.AdminPlacement(ctx, id)
}

// AdminDisputeList — споры администратора (R-10).
func (s *Service) AdminDisputeList(ctx context.Context, state domain.DisputeState, overdue bool, offset, limit int) ([]domain.AdminDisputeListRow, int, error) {
	reads, err := s.adminReads()
	if err != nil {
		return nil, 0, err
	}
	f := domain.AdminDisputeListFilter{State: state, Offset: offset, Limit: limit}
	if f.State == "" {
		f.State = domain.DisputesOpen
	}
	if overdue {
		now := s.Clock.Now()
		f.OverdueAt = &now
	}
	return reads.Disputes(ctx, f)
}

// AdminDisputeByIDView — спор по идентификатору: карточка спора, переписка и размещение.
type AdminDisputeByIDView struct {
	AdminDisputeView
	Messages []domain.DisputeMessage
	Card     AdminPlacementView
}

// AdminDisputeByID — спор для администратора по идентификатору спора; нет — 404.
func (s *Service) AdminDisputeByID(ctx context.Context, id uuid.UUID) (AdminDisputeByIDView, error) {
	reads, err := s.adminReads()
	if err != nil {
		return AdminDisputeByIDView{}, err
	}
	d, err := reads.DisputeByID(ctx, id)
	if err != nil {
		return AdminDisputeByIDView{}, notFound(err, "dispute")
	}
	v, err := s.AdminDispute(ctx, d.PlacementID)
	if err != nil {
		return AdminDisputeByIDView{}, err
	}
	card, err := s.AdminPlacement(ctx, d.PlacementID)
	if err != nil {
		return AdminDisputeByIDView{}, err
	}
	out := AdminDisputeByIDView{AdminDisputeView: v, Card: card}
	if s.Messages != nil {
		if out.Messages, err = s.Messages.List(ctx, d.ID); err != nil {
			return AdminDisputeByIDView{}, err
		}
	}
	return out, nil
}

// DisputePlacementID — размещение спора; нет — 404.
func (s *Service) DisputePlacementID(ctx context.Context, disputeID uuid.UUID) (uuid.UUID, error) {
	reads, err := s.adminReads()
	if err != nil {
		return uuid.Nil, err
	}
	d, err := reads.DisputeByID(ctx, disputeID)
	if err != nil {
		return uuid.Nil, notFound(err, "dispute")
	}
	return d.PlacementID, nil
}

// PreviewResult — предпросмотр решения: доля и распределение удержания.
type PreviewResult struct {
	Resolution   domain.Resolution
	Share        int
	Placement    domain.Placement
	Distribution domain.Distribution
}

// PreviewResolution — распределение по решению без изменений (S-18 шаг 2): те же правила, что
// ResolveDispute (03 §7).
func (s *Service) PreviewResolution(ctx context.Context, disputeID uuid.UUID, res domain.Resolution, share *int) (PreviewResult, error) {
	reads, err := s.adminReads()
	if err != nil {
		return PreviewResult{}, err
	}
	d, err := reads.DisputeByID(ctx, disputeID)
	if err != nil {
		return PreviewResult{}, notFound(err, "dispute")
	}
	p, err := s.Placements.Get(ctx, d.PlacementID)
	if err != nil {
		return PreviewResult{}, err
	}
	v, err := domain.ResolutionShare(res, share, d.SuggestedShare)
	if err != nil {
		return PreviewResult{}, ruleError(err)
	}
	dist, err := domain.Distribute(p.BloggerAmount, p.FeeAmount, p.TotalAmount, v)
	if err != nil {
		return PreviewResult{}, fmt.Errorf("dispute preview: %w", err)
	}
	return PreviewResult{Resolution: res, Share: v, Placement: p, Distribution: dist}, nil
}

// AdminCounts — счётчики модуля для разделов администратора (R-16).
func (s *Service) AdminCounts(ctx context.Context) (domain.AdminCounts, error) {
	reads, err := s.adminReads()
	if err != nil {
		return domain.AdminCounts{}, err
	}
	return reads.Counts(ctx, s.Clock.Now())
}

// ChannelRefs — названия и ссылки каналов для списков администратора.
func (s *Service) ChannelRefs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.ChannelRef, error) {
	offers, err := s.Channels.Offers(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.ChannelRef, len(offers))
	for id, o := range offers {
		out[id] = o.Ref
	}
	return out, nil
}
