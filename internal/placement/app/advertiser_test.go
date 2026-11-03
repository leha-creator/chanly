package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestCancelPlacement(t *testing.T) {
	e := newEnv(t)
	blogger1, user1, ch1 := e.blogger("Первый", 1_000_000)
	_, _, ch2 := e.blogger("Второй", 1_000_000)
	_, _, ch3 := e.blogger("Третий", 1_000_000)
	o := e.order(ch1, ch2, ch3)
	byChannel := map[uuid.UUID]uuid.UUID{}
	for _, pv := range o.Placements {
		byChannel[pv.Placement.ChannelID] = pv.Placement.ID
	}
	p1, p2, p3 := byChannel[ch1], byChannel[ch2], byChannel[ch3]

	// Из created: отменено, удержание снято, T-14 отменён.
	v, err := e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, p3, "cancel-3")
	if err != nil || v.Placement.Status != domain.StatusCancelled || v.Placement.StatusReason != domain.ReasonAdvertiserCancel {
		t.Fatalf("cancel created: %+v %v", v.Placement, err)
	}
	if e.holds.Active(p3) || e.jobStatus(domain.TimerKey(p3, domain.TimerWindow)) != "cancelled" || len(v.Actions) != 0 {
		t.Fatal("hold or T-14 left after cancel")
	}
	// Повтор с тем же ключом — 200 без второй записи истории; без ключа — 409.
	history := e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1`, p3)
	if v, err := e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, p3, "cancel-3"); err != nil || v.Placement.Status != domain.StatusCancelled {
		t.Fatalf("repeat with key: %v", err)
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1`, p3) != history {
		t.Fatal("repeat wrote history")
	}
	_, err = e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, p3, "")
	wantReason(t, err, apperr.CodeInvalidTransition, "")

	e.ready(o)
	// Из awaiting_blogger: T-01 отменён.
	if v, err := e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, p2, ""); err != nil || v.Placement.Status != domain.StatusCancelled {
		t.Fatalf("cancel awaiting blogger: %v", err)
	}
	if e.jobStatus(domain.TimerKey(p2, domain.TimerAccept)) != "cancelled" || e.holds.Active(p2) {
		t.Fatal("T-01 or hold left")
	}
	// Из awaiting_schedule рекламодатель не отменяет.
	if _, err := e.svc.Accept(e.ctx(), blogger1, user1, p1, ""); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CancelPlacement(e.ctx(), e.adv, e.advUser, p1, "")
	wantReason(t, err, apperr.CodeInvalidTransition, "")
	if len(e.logged("placement transition rejected")) != 2 {
		t.Fatal("rejected transitions not logged")
	}
	// Чужое размещение — 404.
	stranger := e.placementOwner()
	if _, err := e.svc.CancelPlacement(e.ctx(), stranger, e.advUser, p1, ""); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign placement: %v", err)
	}
	if e.notices.Count(domain.NoticeCancelled) == 0 || e.count(`SELECT count(*) FROM audit_log WHERE action = 'placement.cancel'`) != 2 {
		t.Fatal("cancel notices or audit")
	}
}

// placementOwner — посторонняя организация.
func (e *env) placementOwner() uuid.UUID {
	org, _ := e.otherAdvertiser()
	return org
}

func TestCancelOrder(t *testing.T) {
	e := newEnv(t)
	blogger1, user1, ch1 := e.blogger("Первый", 1_000_000)
	_, _, ch2 := e.blogger("Второй", 1_000_000)
	o := e.order(ch1, ch2)
	e.ready(o)
	var p1 uuid.UUID
	for _, pv := range o.Placements {
		if pv.Placement.ChannelID == ch1 {
			p1 = pv.Placement.ID
		}
	}
	if _, err := e.svc.Accept(e.ctx(), blogger1, user1, p1, ""); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.CancelOrder(e.ctx(), e.adv, e.advUser, o.Order.ID, "order-cancel")
	if err != nil {
		t.Fatal(err)
	}
	st := map[uuid.UUID]domain.Status{}
	for _, pv := range got.Placements {
		st[pv.Placement.ChannelID] = pv.Placement.Status
	}
	if st[ch1] != domain.StatusAwaitingSchedule || st[ch2] != domain.StatusCancelled || got.Order.Status != domain.OrderInProgress {
		t.Fatalf("cancel order: %v %s", st, got.Order.Status)
	}
	if again, err := e.svc.CancelOrder(e.ctx(), e.adv, e.advUser, o.Order.ID, "order-cancel"); err != nil || again.Order.ID != o.Order.ID {
		t.Fatalf("repeat: %v", err)
	}
	_, err = e.svc.CancelOrder(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	wantReason(t, err, apperr.CodeConflict, "nothing_to_cancel")
}
