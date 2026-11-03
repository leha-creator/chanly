package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

func TestVersionReadySendsToBloggers(t *testing.T) {
	e := newEnv(t)
	_, _, ch1 := e.blogger("Первый", 1_000_000)
	_, _, ch2 := e.blogger("Второй", 1_000_000)
	o := e.order(ch1, ch2)

	e.approve(o)
	if got := e.getOrder(o.Order.ID).Order.Status; got != domain.OrderMarking {
		t.Fatalf("after approve: %s", got)
	}
	// Канал второго блогера приостановлен к моменту P-02 (R-10).
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'suspended' WHERE id = $1`, ch2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.post.MarkVersionReady(context.Background(), o.Version.ID, "E1"); err != nil {
		t.Fatal(err)
	}
	view := e.getOrder(o.Order.ID)
	if view.Order.Status != domain.OrderInProgress {
		t.Fatalf("order %s", view.Order.Status)
	}
	for _, pv := range view.Placements {
		p := pv.Placement
		switch p.ChannelID {
		case ch1:
			if p.Status != domain.StatusAwaitingBlogger || p.AcceptDeadlineAt == nil || !p.AcceptDeadlineAt.Equal(start.Add(24*time.Hour)) ||
				p.SentToBloggerAt == nil {
				t.Fatalf("sent placement %+v", p)
			}
			if e.jobStatus(domain.TimerKey(p.ID, domain.TimerAccept)) != "pending" {
				t.Fatal("T-01 not enqueued")
			}
		case ch2:
			if p.Status != domain.StatusCancelled || p.StatusReason != domain.ReasonChannelUnavailable || e.holds.Active(p.ID) {
				t.Fatalf("unavailable channel placement %+v", p)
			}
		}
	}
	if e.notices.Count(domain.NoticeRequest) != 1 || e.notices.Count(domain.NoticePostApproved) != 1 {
		t.Fatalf("notices %v", e.notices.Templates())
	}
	// Повтор события той же версии ничего не меняет.
	if err := e.svc.VersionChanged(context.Background(), o.Version.ID, domain.VersionReady); err != nil {
		t.Fatal(err)
	}
	if e.notices.Count(domain.NoticeRequest) != 1 || e.count(`SELECT count(*) FROM placement_state_history`) != 4 {
		t.Fatal("repeated event changed placements")
	}
	// Событие по версии, которая не текущая ни в одном заказе, игнорируется.
	if err := e.svc.VersionChanged(context.Background(), uuid.New(), domain.VersionReady); err != nil {
		t.Fatal(err)
	}
	if len(e.logged("version change ignored")) != 1 || len(e.logged("post version change applied")) != 3 {
		t.Fatal("version change log events")
	}
}

func TestVersionReadyAfterWindow(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	e.approve(o)
	// До конца окна 40 минут: вместо P-02 — P-06 window_elapsed.
	e.clk.Set(msk(22, 23, 20))
	if _, err := e.post.MarkVersionReady(context.Background(), o.Version.ID, "E1"); err != nil {
		t.Fatal(err)
	}
	view := e.getOrder(o.Order.ID)
	p := view.Placements[0].Placement
	if p.Status != domain.StatusCancelled || p.StatusReason != domain.ReasonWindowElapsed {
		t.Fatalf("placement %+v", p)
	}
	// Все размещения отменены — заказ cancelled, кампания archived (R-19).
	if view.Order.Status != domain.OrderCancelled {
		t.Fatalf("order %s", view.Order.Status)
	}
	c, _ := e.post.GetCampaign(context.Background(), e.adv, view.Order.CampaignID)
	if c.Status != postdomain.CampaignArchived {
		t.Fatalf("campaign %s", c.Status)
	}
}

func TestRejectionAndNewVersion(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	pid := o.Placements[0].Placement.ID

	// Модерация ещё идёт — новую версию отправить нельзя.
	_, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	wantReason(t, err, apperr.CodeConflict, "post_version_in_progress")

	if _, err := e.post.RejectVersion(context.Background(), o.Version.ID, e.admin(), "Нарушает правила"); err != nil {
		t.Fatal(err)
	}
	view := e.getOrder(o.Order.ID)
	if view.Order.Status != domain.OrderRejected || view.Placements[0].Placement.Status != domain.StatusCreated || e.holds.Active(pid) {
		t.Fatalf("after reject: order %s, placement %s, hold %t", view.Order.Status, view.Placements[0].Placement.Status, e.holds.Active(pid))
	}
	if e.notices.Count(domain.NoticePostRejected) != 1 {
		t.Fatal("rejection notice")
	}

	// Новая версия при нехватке баланса — 409, версия не создана.
	e.holds.SetBalance(e.adv, 100_000)
	versions := e.count(`SELECT count(*) FROM post_version`)
	_, err = e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	wantReason(t, err, apperr.CodeInsufficientBalance, "")
	if e.count(`SELECT count(*) FROM post_version`) != versions || e.holds.Active(pid) {
		t.Fatal("version created or hold taken without balance")
	}

	e.holds.SetBalance(e.adv, 100_000_000)
	e.saveDraft(o.Order.CampaignID, "Исправленный текст")
	got, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "edit-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Order.Status != domain.OrderModeration || got.Version.Number != 2 || got.Order.CurrentPostVersionID == o.Version.ID ||
		got.Placements[0].Placement.PostVersionID != got.Version.ID || !e.holds.Active(pid) {
		t.Fatalf("new version: %+v %+v", got.Order, got.Version)
	}
	// Повтор с тем же ключом — заказ без новой версии.
	again, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "edit-1")
	if err != nil || again.Version.ID != got.Version.ID || e.count(`SELECT count(*) FROM post_version`) != versions+1 {
		t.Fatalf("repeat: %v", err)
	}
	// Событие прежней (не текущей) версии игнорируется.
	if err := e.svc.VersionChanged(context.Background(), o.Version.ID, domain.VersionRejected); err != nil {
		t.Fatal(err)
	}
	if !e.holds.Active(pid) {
		t.Fatal("old version event released hold")
	}
}

func TestNewVersionInProgress(t *testing.T) {
	e := newEnv(t)
	blogger1, user1, ch1 := e.blogger("Первый", 1_000_000)
	_, _, ch2 := e.blogger("Второй", 1_000_000)
	o := e.order(ch1, ch2)
	e.ready(o)
	view := e.getOrder(o.Order.ID)
	var p1, p2 uuid.UUID
	for _, pv := range view.Placements {
		if pv.Placement.ChannelID == ch1 {
			p1 = pv.Placement.ID
		} else {
			p2 = pv.Placement.ID
		}
	}
	// Первое размещение принято и запланировано, второе ждёт блогера.
	if _, err := e.svc.Accept(e.ctx(), blogger1, user1, p1, ""); err != nil {
		t.Fatal(err)
	}
	at := msk(20, 12, 0)
	if _, err := e.svc.Schedule(e.ctx(), blogger1, user1, p1, domain.ModeAt, &at, ""); err != nil {
		t.Fatal(err)
	}
	if e.jobStatus(domain.PublishKey(p1)) != "pending" {
		t.Fatal("publish job not enqueued")
	}
	e.saveDraft(o.Order.CampaignID, "Новый текст")
	got, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, pv := range got.Placements {
		p := pv.Placement
		if p.Status != domain.StatusCreated || p.PostVersionID != got.Version.ID || p.PublishAt != nil || !e.holds.Active(p.ID) {
			t.Fatalf("P-11: %+v", p)
		}
		last := pv.History[len(pv.History)-1]
		if last.Transition != domain.P11ReturnToCreated || last.ReasonCode != domain.ReasonNewPostVersion {
			t.Fatalf("history %+v", last)
		}
	}
	if e.jobStatus(domain.PublishKey(p1)) != "cancelled" || e.jobStatus(domain.RemoveKey(p1)) != "cancelled" ||
		e.jobStatus(domain.TimerKey(p2, domain.TimerAccept)) != "cancelled" {
		t.Fatal("jobs not cancelled on P-11")
	}
	if e.notices.Count(domain.NoticeReturned) != 2 || got.Order.Status != domain.OrderModeration {
		t.Fatalf("notices %v, order %s", e.notices.Templates(), got.Order.Status)
	}
}

func TestNewVersionCancelsShortWindow(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	if _, err := e.post.RejectVersion(context.Background(), o.Version.ID, e.admin(), "Нарушает"); err != nil {
		t.Fatal(err)
	}
	// До конца окна 20 часов (меньше REEDIT_GUARD): размещение отменяется, версия создаётся.
	e.clk.Set(msk(22, 4, 0))
	got, err := e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	p := got.Placements[0].Placement
	if p.Status != domain.StatusCancelled || p.StatusReason != domain.ReasonWindowElapsed || got.Order.Status != domain.OrderCancelled {
		t.Fatalf("short window: %+v %s", p, got.Order.Status)
	}
	_, err = e.svc.CreateOrderPostVersion(e.ctx(), e.adv, e.advUser, o.Order.ID, "")
	wantReason(t, err, apperr.CodeConflict, "nothing_to_update")
}

// R-7: P-02 без активного удержания не выполняется — размещение остаётся в created, ERROR.
func TestVersionReadyWithoutHold(t *testing.T) {
	e := newEnv(t)
	_, _, ch := e.blogger("Канал", 1_000_000)
	o := e.order(ch)
	e.approve(o)
	pid := e.getOrder(o.Order.ID).Placements[0].Placement.ID
	e.holds.Drop(pid)
	if _, err := e.post.MarkVersionReady(context.Background(), o.Version.ID, "E1"); err != nil {
		t.Fatal(err)
	}
	if p := e.getOrder(o.Order.ID).Placements[0].Placement; p.Status != domain.StatusCreated || p.SentToBloggerAt != nil {
		t.Fatalf("placement without hold %+v", p)
	}
	if len(e.logged("placement without active hold")) != 1 || e.notices.Count(domain.NoticeRequest) != 0 {
		t.Fatal("missing hold is not reported")
	}
}
