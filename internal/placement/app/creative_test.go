package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

func TestCreativeRejected(t *testing.T) {
	e := newEnv(t)
	_, _, ch1 := e.blogger("Канал 1", 1_000_000)
	b2, u2, ch2 := e.blogger("Канал 2", 1_000_000)
	b3, u3, ch3 := e.blogger("Канал 3", 1_000_000)
	o := e.order(ch1, ch2, ch3)
	e.ready(o)
	byChannel := map[string]domain.Placement{}
	for _, p := range e.getOrder(o.Order.ID).Placements {
		byChannel[p.Channel.Title] = p.Placement
	}
	waiting, scheduled, published := byChannel["Канал 1"].ID, byChannel["Канал 2"].ID, byChannel["Канал 3"].ID
	for _, a := range []struct{ org, user, pid uuid.UUID }{{b2, u2, scheduled}, {b3, u3, published}} {
		if _, err := e.svc.Accept(e.ctx(), a.org, a.user, a.pid, ""); err != nil {
			t.Fatal(err)
		}
	}
	e.clk.Set(msk(20, 9, 30)) // до срока принятия первого размещения (20 октября 10:00 МСК)
	if _, err := e.svc.Schedule(e.ctx(), b2, u2, scheduled, domain.ModeAt, ptr(msk(21, 12, 0)), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Schedule(e.ctx(), b3, u3, published, domain.ModeNow, nil, ""); err != nil {
		t.Fatal(err)
	}
	e.runJobs()
	if p := e.placement(published); p.Status != domain.StatusPublished {
		t.Fatalf("not published: %s", p.Status)
	}

	got, err := e.svc.CreativeRejected(context.Background(), o.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []uuid.UUID{published}) {
		t.Fatalf("published list %v", got)
	}
	for _, pid := range []domain.Placement{e.placement(waiting), e.placement(scheduled)} {
		if pid.Status != domain.StatusCreated || pid.StatusReason != domain.ReasonErirRejected || pid.PostVersionID != o.Version.ID ||
			pid.PublishAt != nil {
			t.Fatalf("returned placement: %+v", pid)
		}
		if !e.holds.Active(pid.ID) {
			t.Fatalf("hold of %s released", pid.ID)
		}
	}
	if e.jobStatus(domain.PublishKey(scheduled)) != "cancelled" {
		t.Fatalf("publish job of scheduled placement: %s", e.jobStatus(domain.PublishKey(scheduled)))
	}
	if p := e.placement(published); p.Status != domain.StatusPublished {
		t.Fatalf("published placement changed: %s", p.Status)
	}
	if len(e.logged("creative rejected after publication")) != 1 {
		t.Fatal("published placements not logged")
	}

	// Повтор: новых переходов нет.
	history := e.count(`SELECT count(*) FROM placement_state_history`)
	if _, err := e.svc.CreativeRejected(context.Background(), o.Version.ID); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM placement_state_history`) != history {
		t.Fatal("repeat created history entries")
	}
}
