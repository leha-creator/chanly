package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Сценарии над размещением: переход через Apply и его побочные действия (03 §4) —
// поля, задания, отмены и уведомления как значения в StateChange.

// notifyBoth — уведомление обеим сторонам; блогеру — только если размещение ему отправлялось.
func (p *Placement) notifyBoth(template string) []NoticeTo {
	out := []NoticeTo{{Template: template, Role: ActorAdvertiser}}
	if p.SentToBloggerAt != nil {
		out = append(out, NoticeTo{Template: template, Role: ActorBlogger})
	}
	return out
}

// SendToBlogger — версия поста стала ready (P-02). Канал не active — P-06
// channel_unavailable; до конца окна не больше WindowGuard — P-06 window_elapsed (R-10).
// Без версии в ready — ErrVersionNotReady (I-01 в части этапа); без активного удержания —
// ErrNoActiveHold (R-7, I-03).
func (p *Placement) SendToBlogger(channelActive, holdActive bool, version VersionStatus, now time.Time, t Timing) (StateChange, error) {
	if version != VersionReady {
		return StateChange{}, ErrVersionNotReady
	}
	if p.Status == StatusCreated {
		switch {
		case !channelActive:
			return p.Cancel(ActorSystem, ReasonChannelUnavailable, now)
		case p.WindowEnd.Sub(now) <= t.WindowGuard:
			return p.Cancel(ActorSystem, ReasonWindowElapsed, now)
		}
	}
	if !holdActive {
		return StateChange{}, ErrNoActiveHold
	}
	change, err := p.Apply(P02SendToBlogger, ActorSystem, "", now)
	if err != nil {
		return StateChange{}, err
	}
	if p.SentToBloggerAt == nil {
		at := now
		p.SentToBloggerAt = &at
	}
	deadline := AcceptDeadline(now, p.Window(), t)
	p.AcceptDeadlineAt = &deadline
	change.Jobs = []Job{TimerJob(p.ID, TimerAccept, deadline, StatusAwaitingBlogger)}
	if r, ok := ReminderJob(p.ID, deadline, now); ok {
		change.Jobs = append(change.Jobs, r)
	}
	change.Notices = []NoticeTo{{Template: NoticeRequest, Role: ActorBlogger}}
	return change, nil
}

// Accept — блогер принял запрос в работу (P-03): отменить T-01, запустить T-13.
// Проверки канала и исполнителя — в app (I-08, R-3).
func (p *Placement) Accept(userID uuid.UUID, now time.Time, t Timing) (StateChange, error) {
	change, err := p.Apply(P03AcceptByBlogger, ActorBlogger, "", now)
	if err != nil {
		return StateChange{}, err
	}
	deadline := ScheduleDeadline(now, p.Window(), t)
	p.ScheduleDeadlineAt = &deadline
	change.UserID = &userID
	change.CancelJobs = []string{TimerKey(p.ID, TimerAccept), ReminderKey(p.ID)}
	change.Jobs = []Job{TimerJob(p.ID, TimerSchedule, deadline, StatusAwaitingSchedule)}
	change.Notices = []NoticeTo{{Template: NoticeAccepted, Role: ActorAdvertiser}}
	return change, nil
}

// Decline — блогер отказался (P-04). Комментарий — в истории, не в журнале (07 §5.4).
func (p *Placement) Decline(userID uuid.UUID, comment string, now time.Time) (StateChange, error) {
	change, err := p.Apply(P04DeclineByBlogger, ActorBlogger, ReasonBloggerDeclined, now)
	if err != nil {
		return StateChange{}, err
	}
	change.UserID, change.ReasonText = &userID, comment
	change.CancelJobs = []string{TimerKey(p.ID, TimerAccept), ReminderKey(p.ID)}
	change.Notices = []NoticeTo{{Template: NoticeDeclined, Role: ActorAdvertiser}}
	return change, nil
}

// Schedule — блогер выбрал время (P-17): задания публикации и снятия (R-12), отмена T-13.
// Время проверяет CheckPublishAt.
func (p *Placement) Schedule(userID uuid.UUID, publishAt, removeAt, now time.Time) (StateChange, error) {
	change, err := p.Apply(P17Schedule, ActorBlogger, "", now)
	if err != nil {
		return StateChange{}, err
	}
	p.PublishAt, p.RemoveAt = &publishAt, &removeAt
	change.UserID = &userID
	change.CancelJobs = []string{TimerKey(p.ID, TimerSchedule)}
	change.Jobs = []Job{PublishJob(p.ID, publishAt), RemoveJob(p.ID, removeAt)}
	change.Notices = []NoticeTo{{Template: NoticeScheduled, Role: ActorAdvertiser}}
	return change, nil
}

// Reschedule — перенос времени (P-07): только пока до прежнего publish_at больше MinLead.
func (p *Placement) Reschedule(userID uuid.UUID, publishAt, removeAt, now time.Time, t Timing) (StateChange, error) {
	if p.Status == StatusScheduled {
		if err := p.CheckReschedule(now, t); err != nil {
			return StateChange{}, err
		}
	}
	change, err := p.Apply(P07Reschedule, ActorBlogger, "", now)
	if err != nil {
		return StateChange{}, err
	}
	p.PublishAt, p.RemoveAt = &publishAt, &removeAt
	change.UserID = &userID
	change.MoveJobs = []JobMove{{DedupeKey: PublishKey(p.ID), RunAt: publishAt}, {DedupeKey: RemoveKey(p.ID), RunAt: removeAt}}
	change.Notices = []NoticeTo{{Template: NoticeRescheduled, Role: ActorAdvertiser}}
	return change, nil
}

// Cancel — отмена до публикации (P-06): отменить таймеры и задания, уведомить обоих.
func (p *Placement) Cancel(actor ActorType, reason string, now time.Time) (StateChange, error) {
	change, err := p.Apply(P06Cancel, actor, reason, now)
	if err != nil {
		return StateChange{}, err
	}
	change.CancelJobs = allJobKeys(p.ID)
	change.Notices = p.notifyBoth(NoticeCancelled)
	return change, nil
}

// ReturnToCreated — новая версия поста (P-11): время и сроки очищаются, задания отменяются,
// размещение привязывается к новой версии и снова ждёт окна (T-14). Удержание сохраняется.
func (p *Placement) ReturnToCreated(actor ActorType, reason string, versionID uuid.UUID, now time.Time, t Timing) (StateChange, error) {
	change, err := p.Apply(P11ReturnToCreated, actor, reason, now)
	if err != nil {
		return StateChange{}, err
	}
	p.PostVersionID = versionID
	p.PublishAt, p.RemoveAt, p.AcceptDeadlineAt, p.ScheduleDeadlineAt = nil, nil, nil, nil
	change.CancelJobs = []string{TimerKey(p.ID, TimerAccept), TimerKey(p.ID, TimerSchedule), PublishKey(p.ID), RemoveKey(p.ID), ReminderKey(p.ID)}
	change.Jobs = []Job{TimerJob(p.ID, TimerWindow, WindowDeadline(p.Window(), t), StatusCreated)}
	change.Notices = []NoticeTo{{Template: NoticeReturned, Role: ActorBlogger}}
	return change, nil
}

// BindVersion — размещение в created переходит на новую версию поста без перехода (03 §9).
func (p *Placement) BindVersion(versionID uuid.UUID, now time.Time) (prevLockVersion int) {
	prev := p.LockVersion
	p.PostVersionID = versionID
	p.LockVersion++
	p.UpdatedAt = now
	return prev
}

// Expire — сработал таймер: T-01 → P-05, T-13 → P-18, T-14 → P-06 window_elapsed.
func (p *Placement) Expire(timer TimerCode, now time.Time) (StateChange, error) {
	switch timer {
	case TimerAccept:
		change, err := p.Apply(P05AcceptTimeout, ActorSystem, ReasonAcceptTimeout, now)
		if err != nil {
			return StateChange{}, err
		}
		change.Notices = p.notifyBoth(NoticeExpired)
		return change, nil
	case TimerSchedule:
		change, err := p.Apply(P18ScheduleTimeout, ActorSystem, ReasonScheduleTimeout, now)
		if err != nil {
			return StateChange{}, err
		}
		change.Notices = p.notifyBoth(NoticeExpired)
		return change, nil
	case TimerWindow:
		if p.Status != StatusCreated {
			return StateChange{}, apperr.InvalidTransition(string(P06Cancel), string(p.Status))
		}
		return p.Cancel(ActorSystem, ReasonWindowElapsed, now)
	}
	return StateChange{}, apperr.InvalidTransition(string(timer), string(p.Status))
}

// ExpectedStatus — в каком состоянии таймер имеет смысл (03 §5): иначе он устарел.
func (t TimerCode) ExpectedStatus() Status {
	switch t {
	case TimerAccept:
		return StatusAwaitingBlogger
	case TimerSchedule:
		return StatusAwaitingSchedule
	case TimerWindow:
		return StatusCreated
	case TimerClaim:
		return StatusRemoved
	case TimerDispute:
		return StatusDisputed
	}
	return StatusNone
}
