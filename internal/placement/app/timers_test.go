package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/placement/domain"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
)

func TestAcceptTimeout(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	pid := e.sent(ch)
	e.clk.Advance(24*time.Hour + time.Minute)
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusExpired || p.StatusReason != domain.ReasonAcceptTimeout || e.holds.Active(pid) {
		t.Fatalf("T-01: %+v", p)
	}
	var jobID *uuid.UUID
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT job_id FROM placement_state_history WHERE placement_id = $1
		AND transition = 'P-05'`, pid).Scan(&jobID); err != nil || jobID == nil {
		t.Fatalf("history source job: %v %v", jobID, err)
	}
	if len(e.logged("placement timer fired")) != 1 || e.notices.Count(domain.NoticeExpired) != 2 {
		t.Fatalf("timer log or notices: %v", e.notices.Templates())
	}
	// Повтор задания ничего не меняет.
	if err := e.svc.HandleTimer(context.Background(), domain.TimerPayload{PlacementID: pid, Timer: domain.TimerAccept,
		ExpectedStatus: domain.StatusAwaitingBlogger}, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM placement_state_history WHERE placement_id = $1`, pid) != 3 {
		t.Fatal("repeated timer wrote history")
	}
}

func TestOutdatedTimers(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	pid := e.sent(ch)
	// Принял за минуту до T-01: таймер устарел.
	e.clk.Advance(24*time.Hour - time.Minute)
	if _, err := e.svc.Accept(e.ctx(), blogger, user, pid, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.HandleTimer(context.Background(), domain.TimerPayload{PlacementID: pid, Timer: domain.TimerAccept,
		ExpectedStatus: domain.StatusAwaitingBlogger}, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if e.placement(pid).Status != domain.StatusAwaitingSchedule || len(e.logged("placement timer outdated")) != 1 {
		t.Fatal("outdated T-01 changed placement")
	}
	// T-14 в awaiting_schedule — без изменений.
	if err := e.svc.HandleTimer(context.Background(), domain.TimerPayload{PlacementID: pid, Timer: domain.TimerWindow,
		ExpectedStatus: domain.StatusCreated}, uuid.New()); err != nil || e.placement(pid).Status != domain.StatusAwaitingSchedule {
		t.Fatalf("T-14 outside created: %v", err)
	}
	// T-13: срок планирования истёк.
	e.clk.Set(*e.placement(pid).ScheduleDeadlineAt)
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusExpired || p.StatusReason != domain.ReasonScheduleTimeout || e.holds.Active(pid) {
		t.Fatalf("T-13: %+v", p)
	}
}

func TestWindowTimer(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	pid := o.Placements[0].Placement.ID
	e.clk.Set(msk(22, 23, 0))
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusCancelled || p.StatusReason != domain.ReasonWindowElapsed || e.holds.Active(pid) {
		t.Fatalf("T-14: %+v", p)
	}
	if e.getOrder(o.Order.ID).Order.Status != domain.OrderCancelled {
		t.Fatal("order not cancelled")
	}
}

func TestChannelLoss(t *testing.T) {
	e := newEnv(t)
	blogger, user, ch := e.blogger("Канал", 1_000_000)
	created := e.order(ch).Placements[0].Placement.ID
	awaiting := e.sent(ch)
	accepted := e.accepted(blogger, user, ch)
	scheduled := e.accepted(blogger, user, ch)
	if _, err := e.svc.Schedule(e.ctx(), blogger, user, scheduled, domain.ModeAt, ptr(msk(20, 12, 0)), ""); err != nil {
		t.Fatal(err)
	}
	// Карточка каталога показывает занятый топ (R-8 этапа «Каталог»).
	card, err := e.channels.CatalogChannel(context.Background(), ch)
	if err != nil || len(card.Unavailability) != 1 || !card.Unavailability[0].StartsAt.Equal(msk(20, 12, 0)) ||
		!card.Unavailability[0].EndsAt.Equal(msk(20, 13, 0)) {
		t.Fatalf("catalog card busy intervals: %+v %v", card, err)
	}
	if conflict, err := e.svc.ScheduledConflict(context.Background(), ch, msk(21, 0, 0), msk(21, 1, 0)); err != nil || !conflict {
		t.Fatalf("unavailability over scheduled placement: %v %v", conflict, err)
	}

	err = e.tx.InTx(context.Background(), func(ctx context.Context) error {
		return e.svc.ChannelUnavailable(ctx, ch, "active", "suspended")
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]domain.Status{created: domain.StatusCancelled, awaiting: domain.StatusCancelled,
		accepted: domain.StatusCancelled, scheduled: domain.StatusScheduled} {
		p := e.placement(id)
		if p.Status != want || (want == domain.StatusCancelled && p.StatusReason != domain.ReasonChannelUnavailable) {
			t.Errorf("placement %s: %s %s", id, p.Status, p.StatusReason)
		}
	}
	if logs := e.logged("placements cancelled on channel loss"); len(logs) != 1 || logs[0]["count"] != float64(3) {
		t.Fatalf("channel loss log %v", logs)
	}

	// Зависшее scheduled находится через 35 минут после publish_at (07 §3).
	e.clk.Set(msk(20, 12, 34))
	if keys, _ := e.svc.OverdueScheduled(context.Background(), e.clk.Now().Add(-35*time.Minute)); len(keys) != 0 {
		t.Fatalf("too early: %v", keys)
	}
	e.clk.Set(msk(20, 12, 36))
	keys, err := e.svc.OverdueScheduled(context.Background(), e.clk.Now().Add(-35*time.Minute))
	if err != nil || len(keys) != 1 || keys[0] != "placement:"+scheduled.String() {
		t.Fatalf("overdue: %v %v", keys, err)
	}

	// Метрика: число размещений по состояниям (07 §5.5).
	counts, err := e.svc.CountByStatus(context.Background())
	if err != nil || counts[domain.StatusCancelled] != 3 || counts[domain.StatusScheduled] != 1 {
		t.Fatalf("counts %v %v", counts, err)
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(placementpg.NewCollector(e.store))
	families, err := reg.Gather()
	if err != nil || len(families) != 1 || families[0].GetName() != "maxim_placements" || len(families[0].Metric) != 2 {
		t.Fatalf("metric families %v %v", families, err)
	}
}
