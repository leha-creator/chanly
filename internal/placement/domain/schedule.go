package domain

import (
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Interval — полуоткрытый интервал времени [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// Overlaps — интервалы имеют общие точки; стык (End одного = Start другого) — не пересечение.
func (a Interval) Overlaps(b Interval) bool { return a.Start.Before(b.End) && b.Start.Before(a.End) }

// Contains — t внутри интервала.
func (a Interval) Contains(t time.Time) bool { return !t.Before(a.Start) && t.Before(a.End) }

// ScheduleMode — способ выбора времени публикации (S-14).
type ScheduleMode string

const (
	// ModeAt — «Запланировать публикацию» на выбранное время.
	ModeAt ScheduleMode = "at"
	// ModeNow — «Опубликовать сейчас».
	ModeNow ScheduleMode = "now"
)

// TopInterval — интервал топа [publish_at, publish_at + top_hours) (I-06).
func TopInterval(publishAt time.Time, topHours int) Interval {
	return Interval{Start: publishAt, End: publishAt.Add(time.Duration(topHours) * time.Hour)}
}

// CheckPublishAt проверяет выбранное время по R-13 и возвращает publish_at и remove_at.
// mode=at: внутри окна и периода времени суток, не раньше now + MinLead, кратно SlotStep,
// не в интервале недоступности, топ не пересекается с busy (I-06, I-07). mode=now: текущий
// момент внутри окна и периода времени; недоступность и I-06 — от текущего момента.
// busy — интервалы топа других размещений канала в scheduled и published.
func CheckPublishAt(p *Placement, mode ScheduleMode, at *time.Time, now time.Time, busy, unavailable []Interval,
	t Timing) (publishAt, removeAt time.Time, err error) {
	w := p.Window()
	switch mode {
	case ModeAt:
		if at == nil {
			return time.Time{}, time.Time{}, ErrPublishAtRequired
		}
		publishAt = at.UTC()
		if err := checkInWindow(w, publishAt); err != nil {
			return time.Time{}, time.Time{}, err
		}
		if publishAt.Before(now.Add(t.MinLead)) {
			return time.Time{}, time.Time{}, ErrTooSoon
		}
		if !publishAt.Equal(publishAt.Truncate(t.SlotStep)) {
			return time.Time{}, time.Time{}, ErrNotAligned
		}
	case ModeNow:
		publishAt = now.UTC().Truncate(time.Second)
		if err := checkInWindow(w, publishAt); err != nil {
			return time.Time{}, time.Time{}, err
		}
	default:
		return time.Time{}, time.Time{}, ErrUnknownMode
	}
	for _, u := range unavailable {
		if u.Contains(publishAt) {
			return time.Time{}, time.Time{}, ErrChannelUnavailableInterval
		}
	}
	top := TopInterval(publishAt, p.TopHours)
	for _, b := range busy {
		if top.Overlaps(b) {
			return time.Time{}, time.Time{}, ErrSlotTaken
		}
	}
	return publishAt, publishAt.Add(time.Duration(p.DurationHours) * time.Hour), nil
}

func checkInWindow(w Window, at time.Time) error {
	if !w.InDates(at) {
		return ErrOutsideWindow
	}
	if !w.InTimeOfDay(at) {
		return ErrOutsideTime
	}
	return nil
}

// CanReschedule — P-07 допустим: размещение в scheduled, до прежнего publish_at больше MinLead (03 §4).
func (p *Placement) CanReschedule(now time.Time, t Timing) bool {
	return p.Status == StatusScheduled && p.PublishAt != nil && p.PublishAt.Sub(now) > t.MinLead
}

// CheckReschedule — ошибка invalid_transition, если P-07 сейчас недопустим; поздний перенос —
// с details.reason = too_late.
func (p *Placement) CheckReschedule(now time.Time, t Timing) error {
	switch {
	case p.CanReschedule(now, t):
		return nil
	case p.Status == StatusScheduled:
		return apperr.InvalidTransition(string(P07Reschedule), string(p.Status)).WithDetail("reason", "too_late")
	}
	return apperr.InvalidTransition(string(P07Reschedule), string(p.Status))
}
