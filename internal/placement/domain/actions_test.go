package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func jobKeys(js []Job) []string {
	out := make([]string, 0, len(js))
	for _, j := range js {
		out = append(out, j.DedupeKey)
	}
	return out
}

func TestSendToBlogger(t *testing.T) {
	now := mskTime(2026, 10, 20, 10, 0)
	p := testPlacement(StatusCreated)
	if _, err := p.SendToBlogger(true, true, VersionApproved, now, DefaultTiming); !errors.Is(err, ErrVersionNotReady) {
		t.Fatalf("version not ready: %v", err)
	}
	// R-7: без активного удержания P-02 не выполняется, размещение остаётся в created.
	if _, err := p.SendToBlogger(true, false, VersionReady, now, DefaultTiming); !errors.Is(err, ErrNoActiveHold) || p.Status != StatusCreated {
		t.Fatalf("no active hold: %v, status %s", err, p.Status)
	}
	c, err := p.SendToBlogger(true, true, VersionReady, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusAwaitingBlogger || c.Code != P02SendToBlogger || p.SentToBloggerAt == nil || !p.SentToBloggerAt.Equal(now) {
		t.Fatalf("P-02: %+v", p)
	}
	if !p.AcceptDeadlineAt.Equal(now.Add(24*time.Hour)) || !slices.Equal(jobKeys(c.Jobs), []string{TimerKey(p.ID, TimerAccept), ReminderKey(p.ID)}) {
		t.Fatalf("T-01: %v %v", p.AcceptDeadlineAt, jobKeys(c.Jobs))
	}
	if len(c.Notices) != 1 || c.Notices[0].Template != NoticeRequest || c.Notices[0].Role != ActorBlogger {
		t.Fatalf("notices %+v", c.Notices)
	}

	// R-10: до конца окна 40 минут — P-06 window_elapsed; канал не active — channel_unavailable.
	late := testPlacement(StatusCreated)
	c, err = late.SendToBlogger(true, true, VersionReady, late.WindowEnd.Add(-40*time.Minute), DefaultTiming)
	if err != nil || late.Status != StatusCancelled || c.Reason != ReasonWindowElapsed || !c.ReleaseHold || c.Actor != ActorSystem {
		t.Fatalf("window elapsed: %v %+v", err, c)
	}
	off := testPlacement(StatusCreated)
	c, err = off.SendToBlogger(false, true, VersionReady, now, DefaultTiming)
	if err != nil || off.Status != StatusCancelled || c.Reason != ReasonChannelUnavailable {
		t.Fatalf("channel unavailable: %v %+v", err, c)
	}
	if len(c.Notices) != 1 || c.Notices[0].Role != ActorAdvertiser {
		t.Fatalf("blogger never saw the placement and is not notified: %+v", c.Notices)
	}
}

func TestBloggerFlow(t *testing.T) {
	now := mskTime(2026, 10, 20, 10, 0)
	user := uuid.New()
	p := testPlacement(StatusAwaitingBlogger)
	p.SentToBloggerAt = ptrTime(now)
	c, err := p.Accept(user, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusAwaitingSchedule || !p.ScheduleDeadlineAt.Equal(now.Add(24*time.Hour)) || *c.UserID != user ||
		!slices.Equal(c.CancelJobs, []string{TimerKey(p.ID, TimerAccept), ReminderKey(p.ID)}) ||
		!slices.Equal(jobKeys(c.Jobs), []string{TimerKey(p.ID, TimerSchedule)}) {
		t.Fatalf("P-03: %+v %+v", p, c)
	}
	pub, rem := mskTime(2026, 10, 21, 10, 0), mskTime(2026, 10, 22, 10, 0)
	c, err = p.Schedule(user, pub, rem, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusScheduled || !p.PublishAt.Equal(pub) || !p.RemoveAt.Equal(rem) ||
		!slices.Equal(jobKeys(c.Jobs), []string{PublishKey(p.ID), RemoveKey(p.ID)}) || !c.Jobs[0].RunAt.Equal(pub) ||
		!slices.Equal(c.CancelJobs, []string{TimerKey(p.ID, TimerSchedule)}) {
		t.Fatalf("P-17: %+v %+v", p, c)
	}
	pub2, rem2 := pub.Add(time.Hour), rem.Add(time.Hour)
	c, err = p.Reschedule(user, pub2, rem2, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if c.Code != P07Reschedule || !p.PublishAt.Equal(pub2) || len(c.MoveJobs) != 2 || !c.MoveJobs[1].RunAt.Equal(rem2) {
		t.Fatalf("P-07: %+v", c)
	}

	// P-11: время и сроки очищаются, версия новая, задания отменены, T-14 снова.
	v2 := uuid.New()
	c, err = p.ReturnToCreated(ActorAdvertiser, ReasonNewPostVersion, v2, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusCreated || p.PostVersionID != v2 || p.PublishAt != nil || p.RemoveAt != nil ||
		p.AcceptDeadlineAt != nil || p.ScheduleDeadlineAt != nil || c.ReleaseHold {
		t.Fatalf("P-11: %+v", p)
	}
	if !slices.Contains(c.CancelJobs, PublishKey(p.ID)) || !slices.Contains(c.CancelJobs, RemoveKey(p.ID)) ||
		!slices.Equal(jobKeys(c.Jobs), []string{TimerKey(p.ID, TimerWindow)}) {
		t.Fatalf("P-11 jobs: %+v", c)
	}
	if c.Notices[0].Template != NoticeReturned || c.Notices[0].Role != ActorBlogger {
		t.Fatalf("P-11 notice %+v", c.Notices)
	}
}

func TestDeclineKeepsComment(t *testing.T) {
	p := testPlacement(StatusAwaitingBlogger)
	c, err := p.Decline(uuid.New(), "Не подходит тематика", time.Now())
	if err != nil || p.Status != StatusDeclined || c.ReasonText != "Не подходит тематика" || c.Reason != ReasonBloggerDeclined || !c.ReleaseHold {
		t.Fatalf("P-04: %v %+v", err, c)
	}
}

func TestCancelAndExpire(t *testing.T) {
	now := time.Now()
	p := testPlacement(StatusAwaitingSchedule)
	if _, err := p.Cancel(ActorAdvertiser, ReasonAdvertiserCancel, now); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("advertiser cancels accepted placement: %v", err)
	}
	c, err := p.Cancel(ActorSystem, ReasonChannelUnavailable, now)
	if err != nil || len(c.CancelJobs) != 6 {
		t.Fatalf("system cancel: %v %+v", err, c)
	}
	for timer, want := range map[TimerCode]TransitionCode{TimerAccept: P05AcceptTimeout, TimerSchedule: P18ScheduleTimeout, TimerWindow: P06Cancel} {
		p := testPlacement(timer.ExpectedStatus())
		c, err := p.Expire(timer, now)
		if err != nil || c.Code != want || !c.ReleaseHold || c.Actor != ActorSystem {
			t.Errorf("%s: %v %+v", timer, err, c)
		}
	}
	// T-14 в awaiting_blogger устарел.
	p = testPlacement(StatusAwaitingBlogger)
	if _, err := p.Expire(TimerWindow, now); !apperr.Is(err, apperr.CodeInvalidTransition) || p.Status != StatusAwaitingBlogger {
		t.Fatalf("T-14 outdated: %v", err)
	}
	p = testPlacement(StatusCreated)
	prev := p.BindVersion(uuid.New(), now)
	if prev != 1 || p.LockVersion != 2 || p.Status != StatusCreated {
		t.Fatalf("bind version: %d %+v", prev, p)
	}
}

// Этап «Маркировка», R-12: отклонение креатива ЕРИР — P-11 от системы, версия та же.
func TestReturnToCreatedOnErirRejection(t *testing.T) {
	now := mskTime(2026, 10, 20, 10, 0)
	for _, st := range []Status{StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled} {
		p := testPlacement(st)
		version := p.PostVersionID
		c, err := p.ReturnToCreated(ActorSystem, ReasonErirRejected, version, now, DefaultTiming)
		if err != nil || p.Status != StatusCreated || p.StatusReason != ReasonErirRejected || p.PostVersionID != version ||
			c.Code != P11ReturnToCreated {
			t.Fatalf("%s: %+v %v", st, p, err)
		}
	}
	for _, st := range []Status{StatusCreated, StatusPublished} {
		p := testPlacement(st)
		if _, err := p.ReturnToCreated(ActorSystem, ReasonErirRejected, p.PostVersionID, now, DefaultTiming); err == nil {
			t.Fatalf("%s: P-11 allowed", st)
		}
	}
	for _, s := range []Status{StatusPublished, StatusRemoved, StatusDisputed, StatusAccepted, StatusPaid, StatusRefunded} {
		if !s.WasPublished() {
			t.Errorf("%s must be published", s)
		}
	}
	if StatusScheduled.WasPublished() || StatusCancelled.WasPublished() {
		t.Error("scheduled or cancelled reported as published")
	}
}

// Напоминание о T-01 (R-8 этапа уведомлений): за три часа до срока, пока запрос ждёт блогера.
func TestAcceptReminder(t *testing.T) {
	now := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	p := testPlacement(StatusCreated)
	c, err := p.SendToBlogger(true, true, VersionReady, now, DefaultTiming)
	if err != nil {
		t.Fatal(err)
	}
	var reminder *Job
	for i := range c.Jobs {
		if c.Jobs[i].Kind == KindPlacementReminder {
			reminder = &c.Jobs[i]
		}
	}
	if reminder == nil || !reminder.RunAt.Equal(p.AcceptDeadlineAt.Add(-ReminderLead)) || reminder.DedupeKey != ReminderKey(p.ID) {
		t.Fatalf("reminder job %+v", reminder)
	}
	n, ok := p.AcceptReminder(reminder.RunAt)
	if !ok || n.Template != NoticeAcceptReminder || n.Role != ActorBlogger || !strings.HasPrefix(n.Key, ReminderKey(p.ID)+":") {
		t.Fatalf("reminder %+v %v", n, ok)
	}
	if _, ok := p.AcceptReminder(*p.AcceptDeadlineAt); ok {
		t.Fatal("reminder after the deadline")
	}
	if _, err := p.Accept(uuid.New(), reminder.RunAt, DefaultTiming); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.AcceptReminder(reminder.RunAt); ok {
		t.Fatal("reminder after acceptance")
	}
	if _, ok := ReminderJob(p.ID, now.Add(2*time.Hour), now); ok {
		t.Fatal("reminder for a deadline closer than the lead")
	}
}
