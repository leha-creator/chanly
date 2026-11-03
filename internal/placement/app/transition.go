package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// source — источник перехода для истории (03 §12): задание, пользователь, ключ идемпотентности.
type source struct {
	JobID          *uuid.UUID
	InboundEventID *uuid.UUID
	UserID         *uuid.UUID
	Key            string
}

// apply сохраняет результат перехода (I-10): строку размещения «если состояние и
// lock_version равны ожидаемым» (03 §11), затем историю, задания, уведомления и удержание.
// Работает в транзакции вызывающего.
func (s *Service) apply(ctx context.Context, op string, o domain.Order, p domain.Placement, c domain.StateChange, src source) error {
	if err := s.Placements.UpdateIfStatus(ctx, p, c.From, c.PrevLockVersion); err != nil {
		if errors.Is(err, domain.ErrStaleVersion) {
			s.log.WarnContext(ctx, "placement transition rejected", logx.KeyOp, op, KeyPlacementID, p.ID,
				"transition", c.Code, "status", c.From, "reason", "stale")
			return apperr.Conflict("placement was changed concurrently").WithDetail("transition", string(c.Code)).WithCause(err)
		}
		return err
	}
	return s.record(ctx, op, o, p, c, src)
}

// record пишет побочные действия перехода, уже сохранённого (или вставленного при P-01).
func (s *Service) record(ctx context.Context, op string, o domain.Order, p domain.Placement, c domain.StateChange, src source) error {
	if c.UserID == nil {
		c.UserID = src.UserID
	}
	h := domain.NewHistoryEntry(ids.New(), c, src.JobID, src.Key)
	h.InboundEventID = src.InboundEventID
	if err := s.History.Insert(ctx, h); err != nil {
		return err
	}
	for _, key := range c.CancelJobs {
		if _, err := s.Jobs.Cancel(ctx, key); err != nil {
			return err
		}
	}
	for _, m := range c.MoveJobs {
		if _, err := s.Jobs.Reschedule(ctx, m.DedupeKey, m.RunAt); err != nil {
			return err
		}
	}
	for _, j := range c.Jobs {
		if err := s.Jobs.Enqueue(ctx, toJob(j)); err != nil {
			return err
		}
	}
	for _, n := range c.Notices {
		if err := s.Notifier.Notify(ctx, s.notice(o, p, n)); err != nil {
			return err
		}
	}
	if c.ReleaseHold {
		if err := s.Holds.Release(ctx, p.ID, c.Reason); err != nil {
			return err
		}
	}
	s.log.InfoContext(ctx, "placement transition applied", logx.KeyOp, op, KeyPlacementID, p.ID, KeyOrderID, o.ID,
		KeyChannelID, p.ChannelID, "transition", c.Code, "from", c.From, "to", c.To, "actor", c.Actor, "reason", c.Reason)
	return nil
}

// rejected пишет отклонённый переход (WARN) и возвращает err.
func (s *Service) rejected(ctx context.Context, op string, p domain.Placement, code domain.TransitionCode, err error) error {
	reason := rejectReason(err)
	if e, ok := apperr.As(err); ok && e.Code == apperr.CodeInvalidTransition {
		if r, ok := e.Details["reason"].(string); ok {
			reason = r
		} else {
			reason = string(apperr.CodeInvalidTransition)
		}
	}
	s.log.WarnContext(ctx, "placement transition rejected", logx.KeyOp, op, KeyPlacementID, p.ID, "transition", code,
		"status", p.Status, "reason", reason)
	return ruleError(err)
}

// toJob переводит задание домена в задание очереди: попытки и приоритет — по виду (07 §2).
func toJob(j domain.Job) job.Job {
	attempts := unboundedAttempts
	switch j.Kind {
	case domain.KindPlacementTimer:
		attempts = timerAttempts
	case domain.KindCheckPublication:
		attempts = checkAttempts
	case domain.KindPlacementReminder:
		attempts = timerAttempts
	}
	return job.Job{Kind: j.Kind, DedupeKey: j.DedupeKey, RunAt: j.RunAt, Payload: j.Payload, MaxAttempts: attempts}
}

// notice — уведомление стороне размещения: организация по роли (R-4), администратору — без
// организации. Данные — заказ, канал и сроки размещения для текста (R-6 этапа уведомлений);
// данные перехода (адрес публикации) — поверх.
func (s *Service) notice(o domain.Order, p domain.Placement, n domain.NoticeTo) domain.Notice {
	org := o.OrganizationID
	switch n.Role {
	case domain.ActorBlogger:
		org = p.BloggerOrgID
	case domain.ActorAdmin:
		org = uuid.Nil
	}
	data := map[string]any{"order_id": o.ID, "order_number": o.Number, "channel_id": p.ChannelID}
	for key, at := range map[string]*time.Time{"accept_deadline_at": p.AcceptDeadlineAt, "publish_at": p.PublishAt,
		"remove_at": p.RemoveAt, "published_at": p.PublishedAt, "claim_deadline_at": p.ClaimDeadlineAt} {
		if at != nil {
			data[key] = at.UTC()
		}
	}
	if p.FinalViews != nil {
		data["views"] = *p.FinalViews
	}
	for k, v := range n.Data {
		data[k] = v
	}
	return domain.Notice{Template: n.Template, OrganizationID: org, Role: n.Role, ObjectType: "placement",
		ObjectID: p.ID.String(), Key: n.Key, Data: data}
}

// refreshOrder пересчитывает состояние заказа (03 §1.2) по текущей версии из базы.
func (s *Service) refreshOrder(ctx context.Context, op string, o *domain.Order) error {
	v, err := s.Versions.Get(ctx, o.CurrentPostVersionID)
	if err != nil {
		return err
	}
	return s.refreshOrderWith(ctx, op, o, v.Status, false)
}

// refreshOrderWith пересчитывает и сохраняет состояние заказа при известном состоянии версии.
// force — сохранить заказ, даже если состояние не изменилось (новая версия, ключ правки).
// Завершённый заказ архивирует кампанию (R-19).
func (s *Service) refreshOrderWith(ctx context.Context, op string, o *domain.Order, vs domain.VersionStatus, force bool) error {
	ps, err := s.Placements.ListByOrder(ctx, o.ID)
	if err != nil {
		return err
	}
	statuses := make([]domain.Status, 0, len(ps))
	for _, p := range ps {
		statuses = append(statuses, p.Status)
	}
	from := o.Status
	o.Status = domain.DeriveOrderStatus(statuses, vs)
	if o.Status == from && !force {
		return nil
	}
	o.UpdatedAt = s.Clock.Now()
	if err := s.Orders.Update(ctx, *o); err != nil {
		return err
	}
	if o.Status == from {
		return nil
	}
	s.log.InfoContext(ctx, "order status changed", logx.KeyOp, op, KeyOrderID, o.ID, "from", from, "to", o.Status)
	if o.Status.Closed() {
		return s.Campaigns.Archive(ctx, o.OrganizationID, o.CampaignID)
	}
	return nil
}
