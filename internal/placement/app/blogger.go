package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// BloggerPost — пост размещения для блогера: только пока версия в ready, а размещение не
// в created (R-16, 07 §4).
type BloggerPost struct {
	FinalText         string
	MediaURLs         []string
	Erid              string
	ExtraRequirements string
}

// BloggerView — размещение в кабинете блогера.
type BloggerView struct {
	Placement      domain.Placement
	Channel        domain.ChannelRef
	OrderNumber    int64
	AdvertiserName string
	Post           *BloggerPost
	Actions        []string
	// Publication и Dispute — пост в канале и спор, если есть (R-18).
	Publication *domain.Publication
	Dispute     *domain.Dispute
}

// BloggerPage — страница размещений блогера.
type BloggerPage struct {
	Items      []BloggerView
	NextCursor string
}

// ListBloggerPlacements — размещения блогера, отправленные ему хотя бы раз (R-16), новые сверху.
func (s *Service) ListBloggerPlacements(ctx context.Context, orgID uuid.UUID, status *domain.Status, channelID *uuid.UUID,
	cursorStr string, limit int) (BloggerPage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return BloggerPage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.Placements.ListForBlogger(ctx, orgID, status, channelID, after, limit+1)
	if err != nil {
		return BloggerPage{}, err
	}
	page := BloggerPage{}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	page.Items = make([]BloggerView, 0, len(list))
	for _, p := range list {
		v, err := s.bloggerView(ctx, p)
		if err != nil {
			return BloggerPage{}, err
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}

// GetBloggerPlacement — размещение блогера с постом (R-16); чужое или не отправленное — 404.
func (s *Service) GetBloggerPlacement(ctx context.Context, orgID, id uuid.UUID) (BloggerView, error) {
	p, err := s.Placements.GetForBlogger(ctx, orgID, id)
	if err != nil {
		return BloggerView{}, notFound(err, "placement")
	}
	return s.bloggerView(ctx, p)
}

func (s *Service) bloggerView(ctx context.Context, p domain.Placement) (BloggerView, error) {
	o, err := s.Orders.GetByID(ctx, p.OrderID)
	if err != nil {
		return BloggerView{}, err
	}
	v, err := s.Versions.Get(ctx, p.PostVersionID)
	if err != nil {
		return BloggerView{}, err
	}
	offers, err := s.Channels.Offers(ctx, []uuid.UUID{p.ChannelID})
	if err != nil {
		return BloggerView{}, err
	}
	ref := offers[p.ChannelID].Ref
	if ref.ID == uuid.Nil {
		ref.ID = p.ChannelID
	}
	view := BloggerView{Placement: p, Channel: ref, OrderNumber: o.Number, AdvertiserName: v.AdvertiserName,
		Actions: domain.BloggerActions(&p, s.Clock.Now(), s.Config.Timing)}
	pubs, disputes, err := s.control(ctx, []uuid.UUID{p.ID})
	if err != nil {
		return BloggerView{}, err
	}
	if pub, ok := pubs[p.ID]; ok {
		view.Publication = &pub
	}
	if d, ok := disputes[p.ID]; ok {
		view.Dispute = &d
	}
	if p.Status != domain.StatusCreated && v.Status == domain.VersionReady {
		c, err := s.Campaigns.Get(ctx, o.OrganizationID, o.CampaignID)
		if err != nil {
			return BloggerView{}, err
		}
		view.Post = &BloggerPost{FinalText: v.FinalText, MediaURLs: v.MediaURLs, Erid: v.Erid, ExtraRequirements: c.ExtraRequirements}
	}
	return view, nil
}

// bloggerAction — общий ход действия блогера: блокировки в порядке модуля (канал → заказ →
// размещение, R-14), повтор по ключу (R-18), переход act, журнал действий, пересчёт заказа.
func (s *Service) bloggerAction(ctx context.Context, op, action string, orgID, id uuid.UUID, key string, lockChannel bool,
	act func(ctx context.Context, o domain.Order, p *domain.Placement, offer *domain.ChannelOffer) (domain.StateChange, map[string]any, error),
) (BloggerView, error) {
	p0, err := s.Placements.GetForBlogger(ctx, orgID, id)
	if err != nil {
		return BloggerView{}, notFound(err, "placement")
	}
	var out domain.Placement
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var offer *domain.ChannelOffer
		if lockChannel {
			o, err := s.Channels.LockForPlacement(ctx, p0.ChannelID)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err == nil {
				offer = &o
			}
		}
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForBloggerForUpdate(ctx, orgID, id)
		if err != nil {
			return notFound(err, "placement")
		}
		out = p
		if repeated, err := s.repeated(ctx, op, p, key); err != nil || repeated {
			return err
		}
		change, data, err := act(ctx, o, &p, offer)
		if err != nil {
			return err
		}
		if err := s.apply(ctx, op, o, p, change, source{UserID: change.UserID, Key: key}); err != nil {
			return err
		}
		if data == nil {
			data = map[string]any{}
		}
		data["order_id"], data["from"], data["to"] = o.ID, change.From, change.To
		if err := s.Audit.Record(ctx, audit.Entry{Action: action, ObjectType: "placement", ObjectID: p.ID.String(),
			ActorType: audit.ActorBlogger, Data: data}); err != nil {
			return err
		}
		out = p
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return BloggerView{}, err
	}
	return s.bloggerView(ctx, out)
}

// Accept — блогер принял запрос (P-03): канал active (I-08), исполнитель Рокет Ворк (R-3).
func (s *Service) Accept(ctx context.Context, orgID, userID, id uuid.UUID, key string) (BloggerView, error) {
	const op = "Placement.Accept"
	return s.bloggerAction(ctx, op, actionPlacementAccept, orgID, id, key, true,
		func(ctx context.Context, _ domain.Order, p *domain.Placement, offer *domain.ChannelOffer) (domain.StateChange, map[string]any, error) {
			if p.Status == domain.StatusAwaitingBlogger {
				if offer == nil || !offer.Active {
					return domain.StateChange{}, nil, s.acceptBlocked(ctx, op, *p, domain.ErrChannelNotActive)
				}
				ok, err := s.Contractors.CanAccept(ctx, p.BloggerOrgID)
				if err != nil {
					return domain.StateChange{}, nil, err
				}
				if !ok {
					return domain.StateChange{}, nil, s.acceptBlocked(ctx, op, *p, domain.ErrContractorInactive)
				}
			}
			change, err := p.Accept(userID, s.Clock.Now(), s.Config.Timing)
			if err != nil {
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P03AcceptByBlogger, err)
			}
			return change, nil, nil
		})
}

func (s *Service) acceptBlocked(ctx context.Context, op string, p domain.Placement, err *domain.ConflictError) error {
	s.log.WarnContext(ctx, "placement accept blocked", logx.KeyOp, op, KeyPlacementID, p.ID, "reason", err.Reason)
	return ruleError(err)
}

// Decline — блогер отказался (P-04). Комментарий — в истории; в журналы приложения и действий
// не пишется (07 §5.4).
func (s *Service) Decline(ctx context.Context, orgID, userID, id uuid.UUID, comment, key string) (BloggerView, error) {
	const op = "Placement.Decline"
	return s.bloggerAction(ctx, op, actionPlacementDecline, orgID, id, key, false,
		func(ctx context.Context, _ domain.Order, p *domain.Placement, _ *domain.ChannelOffer) (domain.StateChange, map[string]any, error) {
			change, err := p.Decline(userID, comment, s.Clock.Now())
			if err != nil {
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P04DeclineByBlogger, err)
			}
			return change, map[string]any{"has_comment": comment != ""}, nil
		})
}

// Schedule — блогер выбрал время или «Опубликовать сейчас» (P-17): канал active (I-08),
// время по R-13 под блокировкой канала (I-06, R-14); задания публикации и снятия (R-12).
func (s *Service) Schedule(ctx context.Context, orgID, userID, id uuid.UUID, mode domain.ScheduleMode, at *time.Time, key string) (BloggerView, error) {
	const op = "Placement.Schedule"
	return s.bloggerAction(ctx, op, actionPlacementSchedule, orgID, id, key, true,
		func(ctx context.Context, _ domain.Order, p *domain.Placement, offer *domain.ChannelOffer) (domain.StateChange, map[string]any, error) {
			now := s.Clock.Now()
			if p.Status != domain.StatusAwaitingSchedule {
				_, err := p.Schedule(userID, now, now, now)
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P17Schedule, err)
			}
			if offer == nil || !offer.Active {
				s.log.WarnContext(ctx, "placement schedule rejected", logx.KeyOp, op, KeyPlacementID, p.ID, "reason", "channel_not_active")
				return domain.StateChange{}, nil, ruleError(domain.ErrChannelNotActive)
			}
			pub, rem, err := s.checkTime(ctx, op, p, mode, at, now)
			if err != nil {
				return domain.StateChange{}, nil, err
			}
			change, err := p.Schedule(userID, pub, rem, now)
			if err != nil {
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P17Schedule, err)
			}
			return change, map[string]any{"mode": mode, "publish_at": pub, "remove_at": rem}, nil
		})
}

// Reschedule — перенос времени (P-07): пока до прежнего publish_at больше MIN_LEAD; новое время —
// по правилам mode=at (R-13), задания переносятся.
func (s *Service) Reschedule(ctx context.Context, orgID, userID, id uuid.UUID, at time.Time, key string) (BloggerView, error) {
	const op = "Placement.Reschedule"
	return s.bloggerAction(ctx, op, actionPlacementReschedule, orgID, id, key, true,
		func(ctx context.Context, _ domain.Order, p *domain.Placement, _ *domain.ChannelOffer) (domain.StateChange, map[string]any, error) {
			now := s.Clock.Now()
			if err := p.CheckReschedule(now, s.Config.Timing); err != nil {
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P07Reschedule, err)
			}
			pub, rem, err := s.checkTime(ctx, op, p, domain.ModeAt, &at, now)
			if err != nil {
				return domain.StateChange{}, nil, err
			}
			prev := *p.PublishAt
			change, err := p.Reschedule(userID, pub, rem, now, s.Config.Timing)
			if err != nil {
				return domain.StateChange{}, nil, s.rejected(ctx, op, *p, domain.P07Reschedule, err)
			}
			return change, map[string]any{"publish_at_before": prev, "publish_at": pub, "remove_at": rem}, nil
		})
}

// checkTime — выбор времени по R-13: занятые топы канала (без самого размещения) и интервалы
// недоступности читаются под блокировкой канала.
func (s *Service) checkTime(ctx context.Context, op string, p *domain.Placement, mode domain.ScheduleMode, at *time.Time,
	now time.Time) (time.Time, time.Time, error) {
	from, to := now.Add(-24*time.Hour), p.WindowEnd.Add(24*time.Hour)
	busy, err := s.Placements.BusyIntervals(ctx, p.ChannelID, from, to, p.ID)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	unavailable, err := s.Channels.Unavailability(ctx, p.ChannelID, from, to)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	pub, rem, err := domain.CheckPublishAt(p, mode, at, now, busy, unavailable, s.Config.Timing)
	if err != nil {
		s.log.WarnContext(ctx, "placement schedule rejected", logx.KeyOp, op, KeyPlacementID, p.ID, "reason", rejectReason(err),
			"mode", mode)
		return time.Time{}, time.Time{}, ruleError(err)
	}
	return pub, rem, nil
}
