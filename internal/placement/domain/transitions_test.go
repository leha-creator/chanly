package domain

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// spec — таблица 03 §4, переписанная независимо от transitions: переход, кто и из
// каких состояний запускает, новое состояние.
var spec = []struct {
	code TransitionCode
	to   []Status
	who  map[ActorType][]Status
}{
	{P01Create, []Status{StatusCreated}, map[ActorType][]Status{ActorAdvertiser: {StatusNone}}},
	{P02SendToBlogger, []Status{StatusAwaitingBlogger}, map[ActorType][]Status{ActorSystem: {StatusCreated}}},
	{P03AcceptByBlogger, []Status{StatusAwaitingSchedule}, map[ActorType][]Status{ActorBlogger: {StatusAwaitingBlogger}}},
	{P04DeclineByBlogger, []Status{StatusDeclined}, map[ActorType][]Status{ActorBlogger: {StatusAwaitingBlogger}}},
	{P05AcceptTimeout, []Status{StatusExpired}, map[ActorType][]Status{ActorSystem: {StatusAwaitingBlogger}}},
	{P06Cancel, []Status{StatusCancelled}, map[ActorType][]Status{
		ActorAdvertiser: {StatusCreated, StatusAwaitingBlogger},
		ActorAdmin:      {StatusCreated, StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled},
		ActorSystem:     {StatusCreated, StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled},
	}},
	{P07Reschedule, []Status{StatusScheduled}, map[ActorType][]Status{ActorBlogger: {StatusScheduled}}},
	{P08Publish, []Status{StatusPublished}, map[ActorType][]Status{ActorSystem: {StatusScheduled}}},
	{P09PublishFailed, []Status{StatusFailed}, map[ActorType][]Status{ActorSystem: {StatusScheduled}}},
	{P10Remove, []Status{StatusRemoved}, map[ActorType][]Status{ActorSystem: {StatusPublished}, ActorAdmin: {StatusPublished}}},
	{P11ReturnToCreated, []Status{StatusCreated}, map[ActorType][]Status{
		ActorAdvertiser: {StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled},
		ActorSystem:     {StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled},
	}},
	{P12AcceptByAdvertiser, []Status{StatusAccepted}, map[ActorType][]Status{ActorAdvertiser: {StatusRemoved}}},
	{P13AutoAccept, []Status{StatusAccepted}, map[ActorType][]Status{ActorSystem: {StatusRemoved}}},
	{P14OpenDispute, []Status{StatusDisputed}, map[ActorType][]Status{
		ActorAdvertiser: {StatusPublished, StatusRemoved},
		ActorSystem:     {StatusPublished, StatusRemoved},
		ActorAdmin:      {StatusPublished, StatusRemoved},
	}},
	{P15ResolveDispute, []Status{StatusAccepted, StatusRefunded}, map[ActorType][]Status{ActorAdmin: {StatusDisputed}}},
	{P16Paid, []Status{StatusPaid}, map[ActorType][]Status{ActorSystem: {StatusAccepted}}},
	{P17Schedule, []Status{StatusScheduled}, map[ActorType][]Status{ActorBlogger: {StatusAwaitingSchedule}}},
	{P18ScheduleTimeout, []Status{StatusExpired}, map[ActorType][]Status{ActorSystem: {StatusAwaitingSchedule}}},
}

func TestTransitionTableMatchesSpec(t *testing.T) {
	if len(spec) != len(transitions) || len(TransitionCodes) != 18 {
		t.Fatalf("table has %d transitions, spec %d, codes %d", len(transitions), len(spec), len(TransitionCodes))
	}
	all := append([]Status{StatusNone}, Statuses...)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for i, s := range spec {
		if TransitionCodes[i] != s.code {
			t.Fatalf("code order: %s at %d, want %s", TransitionCodes[i], i, s.code)
		}
		if got := transitions[s.code].to; !slices.Equal(got, s.to) {
			t.Errorf("%s: to %v, want %v", s.code, got, s.to)
		}
		for _, actor := range ActorTypes {
			for _, from := range all {
				want := slices.Contains(s.who[actor], from)
				if got := Allowed(s.code, actor, from); got != want {
					t.Errorf("%s by %s from %q: allowed %t, want %t", s.code, actor, from, got, want)
				}
				p := Placement{ID: uuid.New(), Status: from, LockVersion: 3}
				change, err := p.ApplyTo(s.code, s.to[0], actor, "r", now)
				if !want {
					if !apperr.Is(err, apperr.CodeInvalidTransition) {
						t.Errorf("%s by %s from %q: want invalid_transition, got %v", s.code, actor, from, err)
					} else if e, _ := apperr.As(err); e.Details["status"] != string(from) || e.Details["transition"] != string(s.code) {
						t.Errorf("%s: details %v", s.code, e.Details)
					}
					if p.Status != from || p.LockVersion != 3 {
						t.Errorf("%s: rejected transition changed placement", s.code)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s by %s from %q: %v", s.code, actor, from, err)
				}
				if p.Status != s.to[0] || p.LockVersion != 4 || !p.StatusChangedAt.Equal(now) || p.StatusReason != "r" {
					t.Errorf("%s: placement after apply %+v", s.code, p)
				}
				if change.From != from || change.To != s.to[0] || change.PrevLockVersion != 3 || change.Code != s.code ||
					change.Actor != actor || change.ReleaseHold != s.to[0].ReleasesHold() {
					t.Errorf("%s: change %+v", s.code, change)
				}
			}
		}
	}
}

func TestTerminalStatusesAreFinal(t *testing.T) {
	want := []Status{StatusPaid, StatusDeclined, StatusExpired, StatusCancelled, StatusFailed, StatusRefunded}
	for _, s := range Statuses {
		if s.Terminal() != slices.Contains(want, s) {
			t.Errorf("%s: terminal %t", s, s.Terminal())
		}
		if !s.Terminal() {
			continue
		}
		for _, code := range TransitionCodes {
			for _, actor := range ActorTypes {
				if Allowed(code, actor, s) {
					t.Errorf("I-04: %s by %s leaves terminal %s", code, actor, s)
				}
			}
		}
	}
	if StatusPaid.ReleasesHold() || !StatusRefunded.ReleasesHold() || StatusScheduled.ReleasesHold() {
		t.Error("hold release: paid keeps distribution, refunded releases, scheduled keeps hold")
	}
}

func TestApplyResolveDisputeNeedsTarget(t *testing.T) {
	p := Placement{Status: StatusDisputed}
	if _, err := p.Apply(P15ResolveDispute, ActorAdmin, "", time.Now()); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("P-15 without outcome: %v", err)
	}
	if _, err := p.ApplyTo(P15ResolveDispute, StatusPaid, ActorAdmin, "", time.Now()); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("P-15 to paid: %v", err)
	}
	if _, err := p.ApplyTo(P15ResolveDispute, StatusRefunded, ActorAdmin, "", time.Now()); err != nil || p.Status != StatusRefunded {
		t.Fatalf("P-15 to refunded: %v %s", err, p.Status)
	}
	if _, err := p.Apply("P-99", ActorSystem, "", time.Now()); !apperr.Is(err, apperr.CodeInvalidTransition) {
		t.Fatalf("unknown code: %v", err)
	}
}
