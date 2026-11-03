package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// removed — опубликованное размещение, снятое по сроку; окно претензий открыто.
func (e *env) removed() uuid.UUID {
	e.t.Helper()
	pid, _, _ := e.published()
	e.clk.Set(e.placement(pid).RemoveAt.Add(4 * time.Second))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusRemoved {
		e.t.Fatalf("not removed: %s", p.Status)
	}
	return pid
}

func TestAcceptPlacement(t *testing.T) {
	e := newEnv(t)
	pid := e.removed()
	v, err := e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "accept-1")
	if err != nil {
		t.Fatal(err)
	}
	p := e.placement(pid)
	if v.Placement.Status != domain.StatusAccepted || p.Status != domain.StatusAccepted || *p.PerformanceShare != 10000 ||
		*p.PayoutAmount != p.BloggerAmount || *p.RefundAmount != 0 {
		t.Fatalf("accepted %+v", p)
	}
	if by, ok := e.holds.SettledBy(pid); !ok || by.Actor != domain.ActorAdvertiser || *by.UserID != e.advUser {
		t.Fatalf("settled by %+v", by)
	}
	if got := e.payouts.Created(); len(got) != 1 || got[0].Amount != p.BloggerAmount || got[0].PlacementID != pid {
		t.Fatalf("payouts %+v", got)
	}
	if s := e.jobStatus(domain.TimerKey(pid, domain.TimerClaim)); s != "cancelled" {
		t.Fatalf("T-05 %s", s)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'placement.accept_work' AND object_id = $1`, pid.String()) != 1 {
		t.Fatal("audit")
	}
	// Повтор с тем же ключом — без нового перехода и второй выплаты.
	if _, err := e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "accept-1"); err != nil {
		t.Fatal(err)
	}
	if len(e.payouts.Created()) != 1 {
		t.Fatal("second payout")
	}
	// Без ключа — 409: из accepted приёмка недопустима.
	_, err = e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "")
	wantReason(t, err, apperr.CodeInvalidTransition, "")
	// Чужое размещение — 404.
	other, otherUser := e.otherAdvertiser()
	_, err = e.svc.AcceptPlacement(e.ctx(), other, otherUser, pid, "")
	wantReason(t, err, apperr.CodeNotFound, "")
}

func TestAcceptPublishedRejected(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	_, err := e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "")
	wantReason(t, err, apperr.CodeInvalidTransition, "")
	if e.placement(pid).Status != domain.StatusPublished || len(e.payouts.Created()) != 0 {
		t.Fatal("state changed")
	}
}

func TestAcceptLivePublicationRejected(t *testing.T) {
	e := newEnv(t)
	pid := e.removed()
	// I-02: размещение в removed, а публикация жива (рассогласование) — отказ publication_live.
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE publication SET removed_at = NULL, removal_reason = NULL
		WHERE placement_id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.AcceptPlacement(e.ctx(), e.adv, e.advUser, pid, "")
	wantReason(t, err, apperr.CodeInvalidTransition, domain.ReasonPublicationLive)
	if e.placement(pid).Status != domain.StatusRemoved || len(e.payouts.Created()) != 0 {
		t.Fatal("state changed")
	}
	if len(e.logged("placement invariant violated")) != 1 {
		t.Fatal("invariant not logged")
	}
}

func TestAutoAcceptOutdated(t *testing.T) {
	e := newEnv(t)
	pid, chatID, _ := e.published()
	e.topViolation(pid, chatID)
	if e.placement(pid).Status != domain.StatusDisputed {
		t.Fatal("not disputed")
	}
	// Снятие по сроку в споре: состояние остаётся disputed, T-05 не ставится или устарел.
	e.clk.Set(e.placement(pid).RemoveAt.Add(25 * time.Hour))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusDisputed || len(e.payouts.Created()) != 0 {
		t.Fatalf("disputed placement auto accepted: %s", p.Status)
	}
}

func TestDevAcceptSettles(t *testing.T) {
	e := newEnv(t)
	pid := e.removed()
	payoutID, err := e.svc.DevAccept(e.ctx(), pid, 7500)
	if err != nil || payoutID == uuid.Nil {
		t.Fatalf("dev accept %v %v", payoutID, err)
	}
	p := e.placement(pid)
	if p.Status != domain.StatusAccepted || *p.PerformanceShare != 7500 {
		t.Fatalf("dev accept %+v", p)
	}
	if s := e.jobStatus(domain.TimerKey(pid, domain.TimerClaim)); s != "cancelled" {
		t.Fatalf("T-05 %s", s)
	}
}
