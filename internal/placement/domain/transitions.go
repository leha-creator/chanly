package domain

import (
	"slices"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// TransitionCode — номер перехода размещения (03 §4).
type TransitionCode string

const (
	P01Create             TransitionCode = "P-01"
	P02SendToBlogger      TransitionCode = "P-02"
	P03AcceptByBlogger    TransitionCode = "P-03"
	P04DeclineByBlogger   TransitionCode = "P-04"
	P05AcceptTimeout      TransitionCode = "P-05"
	P06Cancel             TransitionCode = "P-06"
	P07Reschedule         TransitionCode = "P-07"
	P08Publish            TransitionCode = "P-08"
	P09PublishFailed      TransitionCode = "P-09"
	P10Remove             TransitionCode = "P-10"
	P11ReturnToCreated    TransitionCode = "P-11"
	P12AcceptByAdvertiser TransitionCode = "P-12"
	P13AutoAccept         TransitionCode = "P-13"
	P14OpenDispute        TransitionCode = "P-14"
	P15ResolveDispute     TransitionCode = "P-15"
	P16Paid               TransitionCode = "P-16"
	P17Schedule           TransitionCode = "P-17"
	P18ScheduleTimeout    TransitionCode = "P-18"
)

// TransitionCodes — все переходы по порядку 03 §4.
var TransitionCodes = []TransitionCode{
	P01Create, P02SendToBlogger, P03AcceptByBlogger, P04DeclineByBlogger, P05AcceptTimeout, P06Cancel, P07Reschedule,
	P08Publish, P09PublishFailed, P10Remove, P11ReturnToCreated, P12AcceptByAdvertiser, P13AutoAccept, P14OpenDispute,
	P15ResolveDispute, P16Paid, P17Schedule, P18ScheduleTimeout,
}

// rule — кто из каких состояний может выполнить переход.
type rule struct {
	actor ActorType
	from  []Status
}

// transition — строка таблицы 03 §4: правила запуска и допустимые новые состояния.
type transition struct {
	rules []rule
	to    []Status
}

var (
	beforePublish  = []Status{StatusCreated, StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled}
	sentToBlogger  = []Status{StatusAwaitingBlogger, StatusAwaitingSchedule, StatusScheduled}
	publishedLater = []Status{StatusPublished, StatusRemoved}
)

// transitions — единственная таблица переходов (07 §9). Сверяется с 03 §4 тестом.
var transitions = map[TransitionCode]transition{
	P01Create: {rules: []rule{{ActorAdvertiser, []Status{StatusNone}}}, to: []Status{StatusCreated}},
	P02SendToBlogger: {rules: []rule{{ActorSystem, []Status{StatusCreated}}},
		to: []Status{StatusAwaitingBlogger}},
	P03AcceptByBlogger: {rules: []rule{{ActorBlogger, []Status{StatusAwaitingBlogger}}},
		to: []Status{StatusAwaitingSchedule}},
	P04DeclineByBlogger: {rules: []rule{{ActorBlogger, []Status{StatusAwaitingBlogger}}},
		to: []Status{StatusDeclined}},
	P05AcceptTimeout: {rules: []rule{{ActorSystem, []Status{StatusAwaitingBlogger}}},
		to: []Status{StatusExpired}},
	// Рекламодатель отменяет только до принятия (03 §4, S-07).
	P06Cancel: {rules: []rule{
		{ActorAdvertiser, []Status{StatusCreated, StatusAwaitingBlogger}},
		{ActorAdmin, beforePublish},
		{ActorSystem, beforePublish},
	}, to: []Status{StatusCancelled}},
	P07Reschedule: {rules: []rule{{ActorBlogger, []Status{StatusScheduled}}},
		to: []Status{StatusScheduled}},
	P08Publish: {rules: []rule{{ActorSystem, []Status{StatusScheduled}}},
		to: []Status{StatusPublished}},
	P09PublishFailed: {rules: []rule{{ActorSystem, []Status{StatusScheduled}}},
		to: []Status{StatusFailed}},
	// admin_mark_removed — администратор (03 §4).
	P10Remove: {rules: []rule{{ActorSystem, []Status{StatusPublished}}, {ActorAdmin, []Status{StatusPublished}}},
		to: []Status{StatusRemoved}},
	// Новая версия поста (S-29) или отказ ЕРИР (S-28).
	P11ReturnToCreated: {rules: []rule{{ActorAdvertiser, sentToBlogger}, {ActorSystem, sentToBlogger}},
		to: []Status{StatusCreated}},
	P12AcceptByAdvertiser: {rules: []rule{{ActorAdvertiser, []Status{StatusRemoved}}},
		to: []Status{StatusAccepted}},
	P13AutoAccept: {rules: []rule{{ActorSystem, []Status{StatusRemoved}}},
		to: []Status{StatusAccepted}},
	P14OpenDispute: {rules: []rule{
		{ActorAdvertiser, publishedLater}, {ActorSystem, publishedLater}, {ActorAdmin, publishedLater},
	}, to: []Status{StatusDisputed}},
	P15ResolveDispute: {rules: []rule{{ActorAdmin, []Status{StatusDisputed}}},
		to: []Status{StatusAccepted, StatusRefunded}},
	P16Paid: {rules: []rule{{ActorSystem, []Status{StatusAccepted}}},
		to: []Status{StatusPaid}},
	P17Schedule: {rules: []rule{{ActorBlogger, []Status{StatusAwaitingSchedule}}},
		to: []Status{StatusScheduled}},
	P18ScheduleTimeout: {rules: []rule{{ActorSystem, []Status{StatusAwaitingSchedule}}},
		to: []Status{StatusExpired}},
}

// Allowed — переход code допустим для actor из состояния from.
func Allowed(code TransitionCode, actor ActorType, from Status) bool {
	t, ok := transitions[code]
	if !ok {
		return false
	}
	for _, r := range t.rules {
		if r.actor == actor && slices.Contains(r.from, from) {
			return true
		}
	}
	return false
}

// Apply — единственный способ сменить состояние размещения: проверка состояния и
// актора, новое состояние, причина, lock_version. Переход с несколькими исходами (P-15)
// требует ApplyTo.
func (p *Placement) Apply(code TransitionCode, actor ActorType, reason string, now time.Time) (StateChange, error) {
	return p.ApplyTo(code, StatusNone, actor, reason, now)
}

// ApplyTo — Apply с выбором нового состояния из допустимых (P-15: accepted или refunded).
// to = StatusNone — единственное допустимое состояние.
func (p *Placement) ApplyTo(code TransitionCode, to Status, actor ActorType, reason string, now time.Time) (StateChange, error) {
	t, ok := transitions[code]
	if !ok || !Allowed(code, actor, p.Status) {
		return StateChange{}, apperr.InvalidTransition(string(code), string(p.Status))
	}
	switch {
	case to == StatusNone && len(t.to) == 1:
		to = t.to[0]
	case !slices.Contains(t.to, to):
		return StateChange{}, apperr.InvalidTransition(string(code), string(p.Status))
	}
	change := StateChange{
		PlacementID: p.ID, OrderID: p.OrderID, Code: code, From: p.Status, To: to, Actor: actor,
		Reason: reason, At: now, PrevLockVersion: p.LockVersion, ReleaseHold: to.ReleasesHold(),
	}
	p.Status, p.StatusChangedAt, p.StatusReason = to, now, reason
	p.LockVersion++
	p.UpdatedAt = now
	return change, nil
}
