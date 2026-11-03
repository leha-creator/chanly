package app_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

// published — опубликованное размещение (20 октября 11:30 МСК) и его сообщение в MAX.
func (e *env) published() (pid uuid.UUID, chatID int64, msgID string) {
	e.t.Helper()
	pid, chatID = e.scheduledNow(0)
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusPublished {
		e.t.Fatalf("not published: %s", p.Status)
	}
	return pid, chatID, e.publication(pid).MaxMessageID
}

func TestRemoveOnSchedule(t *testing.T) {
	e := newEnv(t)
	pid, chatID, msgID := e.published()
	e.max.SetViews(msgID, 1200)
	removeAt := *e.placement(pid).RemoveAt
	e.clk.Set(removeAt.Add(4 * time.Second))
	e.runJobs()

	p := e.placement(pid)
	if p.Status != domain.StatusRemoved || p.FinalViews == nil || *p.FinalViews != 1200 || !p.RemovedAt.Equal(e.clk.Now()) {
		t.Fatalf("removed: %s views %v at %v", p.Status, p.FinalViews, p.RemovedAt)
	}
	claim := p.RemovedAt.Add(24 * time.Hour)
	if !p.ClaimDeadlineAt.Equal(claim) || !e.jobRunAt(domain.TimerKey(pid, domain.TimerClaim)).Equal(claim) {
		t.Fatalf("claim deadline %v", p.ClaimDeadlineAt)
	}
	pub := e.publication(pid)
	if pub.Live() || pub.RemovalReason != domain.RemovalSchedule || pub.RemovalOverdue || *pub.Views != 1200 {
		t.Fatalf("publication %+v", pub)
	}
	if !slices.Contains(e.max.Deleted(), msgID) || e.notices.Count(domain.NoticeRemoved) != 2 {
		t.Fatalf("deleted %v notices %v", e.max.Deleted(), e.notices.Templates())
	}
	// Событие о своём удалении нарушением не считается (05 §2.2).
	e.event(removedEvent(chatID, msgID, e.clk.Now()))
	e.runJobs()
	if e.count(`SELECT count(*) FROM violation`) != 0 || e.placement(pid).Status != domain.StatusRemoved {
		t.Fatal("own removal reported as violation")
	}
	if len(e.logged("placement removed")) != 1 {
		t.Fatal("removal not logged")
	}

	// T-05: окно претензий истекло — P-13, полная доля, расчёт от имени системы.
	e.clk.Set(claim.Add(time.Second))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusAccepted || *p.PerformanceShare != 10000 {
		t.Fatalf("T-05: %s", p.Status)
	}
	if by, ok := e.holds.SettledBy(pid); !ok || by.Actor != domain.ActorSystem {
		t.Fatalf("settled by %+v %t", by, ok)
	}
}

func TestRemoveInDisputeKeepsState(t *testing.T) {
	e := newEnv(t)
	pid, chatID, msgID := e.published()
	e.topViolation(pid, chatID)
	if p := e.placement(pid); p.Status != domain.StatusDisputed {
		t.Fatalf("dispute: %s", p.Status)
	}
	e.clk.Set(e.placement(pid).RemoveAt.Add(time.Second))
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusDisputed || !slices.Contains(e.max.Deleted(), msgID) || e.publication(pid).Live() {
		t.Fatalf("removal in dispute: %s deleted %v", p.Status, e.max.Deleted())
	}
}

func TestRemoveFailureOverdue(t *testing.T) {
	e := newEnv(t)
	pid, _, _ := e.published()
	e.max.FailDeletesWith(errors.New("max: status 503"))
	removeAt := *e.placement(pid).RemoveAt
	e.clk.Set(removeAt)
	e.runJobs()
	pub := e.publication(pid)
	if !pub.Live() || !pub.RemovalOverdue || e.placement(pid).Status != domain.StatusPublished {
		t.Fatalf("after failure: %+v", pub)
	}
	first := e.jobRunAt(domain.RemoveKey(pid))
	e.clk.Set(first)
	e.runJobs()
	if second := e.jobRunAt(domain.RemoveKey(pid)); second.Sub(first) <= 30*time.Second {
		t.Fatalf("retry delay did not grow: %v then %v", first, second)
	}
	// Через 24 ч от remove_at — без повторов, оповещение и уведомление блогеру.
	e.clk.Set(removeAt.Add(24*time.Hour + time.Minute))
	if _, err := e.d.Pool.Exec(e.ctx(), `UPDATE job SET run_at = $2 WHERE dedupe_key = $1 AND status = 'pending'`,
		domain.RemoveKey(pid), e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	e.runJobs()
	if st := e.jobStatus(domain.RemoveKey(pid)); st != "succeeded" {
		t.Fatalf("remove job after T-10: %s", st)
	}
	if !slices.Contains(e.alerts.reasons(), "publication_removal_overdue") || e.notices.Count(domain.NoticeRemovalOverdue) != 1 {
		t.Fatalf("alerts %v notices %v", e.alerts.reasons(), e.notices.Templates())
	}
	if !e.publication(pid).RemovalOverdue {
		t.Fatal("overdue flag cleared")
	}
	// Зависшее снятие находится через час после отметки (07 §3).
	keys, err := e.svc.OverdueRemovals(e.ctx(), e.clk.Now().Add(time.Hour))
	if err != nil || len(keys) != 1 {
		t.Fatalf("overdue removals %v %v", keys, err)
	}
	if keys, _ := e.svc.OverdueRemovals(e.ctx(), removeAt); len(keys) != 0 {
		t.Fatalf("overdue before threshold %v", keys)
	}
}

func TestRemoveFindsPostGone(t *testing.T) {
	e := newEnv(t)
	pid, _, msgID := e.published()
	e.max.RemoveExternally(msgID)
	e.clk.Set(e.placement(pid).RemoveAt.Add(time.Second))
	e.runJobs()
	p := e.placement(pid)
	if p.Status != domain.StatusDisputed || p.StatusReason != string(domain.ViolationRemovedEarly) {
		t.Fatalf("post gone at removal: %s %s", p.Status, p.StatusReason)
	}
	if pub := e.publication(pid); pub.RemovalReason != domain.RemovalExternal {
		t.Fatalf("publication %+v", pub)
	}
}
