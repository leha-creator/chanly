package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

// reconcile запускает сверку и проверки публикаций, сдвигая часы на интервал сверки.
func (e *env) reconcile() {
	e.t.Helper()
	if err := e.svc.ReconcilePublications(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.clk.Advance(5 * time.Minute)
	e.runJobs()
}

func TestReconcileSpreadsChecks(t *testing.T) {
	e := newEnv(t)
	for range 3 {
		e.published()
	}
	now := e.clk.Now()
	if err := e.svc.ReconcilePublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ReconcilePublications(context.Background()); err != nil { // повтор не ставит дубли
		t.Fatal(err)
	}
	rows, err := e.d.Pool.Query(context.Background(), `SELECT run_at FROM job WHERE kind = 'check_publication' AND status = 'pending'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[time.Time]bool{}
	for rows.Next() {
		var at time.Time
		_ = rows.Scan(&at)
		if at.Before(now) || !at.Before(now.Add(5*time.Minute)) {
			t.Fatalf("check at %v outside interval", at)
		}
		seen[at] = true
	}
	if len(seen) != 3 {
		t.Fatalf("checks %d, want 3 distinct", len(seen))
	}
}

func TestReconcileFindsViolationsWithoutEvents(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		e := newEnv(t)
		pid, _, msgID := e.published()
		e.max.RemoveExternally(msgID)
		e.reconcile()
		if p := e.placement(pid); p.Status != domain.StatusDisputed || p.StatusReason != "removed_early" {
			t.Fatalf("removed: %s %s", p.Status, p.StatusReason)
		}
		if e.count(`SELECT count(*) FROM violation WHERE source = 'reconciliation'`) != 1 {
			t.Fatal("violation source")
		}
	})
	t.Run("edited", func(t *testing.T) {
		e := newEnv(t)
		pid, _, msgID := e.published()
		e.max.EditExternally(msgID)
		e.reconcile()
		if p := e.placement(pid); p.StatusReason != "content_modified" {
			t.Fatalf("edited: %s %s", p.Status, p.StatusReason)
		}
	})
	t.Run("top", func(t *testing.T) {
		e := newEnv(t)
		pid, chatID, _ := e.published()
		human := e.placement(pid).PublishedAt.Add(3 * time.Minute)
		e.max.PostAsHuman(chatID, 42, human)
		e.reconcile()
		p := e.placement(pid)
		if p.StatusReason != "top_violated" {
			t.Fatalf("top: %s %s", p.Status, p.StatusReason)
		}
		var occurred time.Time
		if err := e.d.Pool.QueryRow(context.Background(), `SELECT occurred_at FROM violation`).Scan(&occurred); err != nil || !occurred.Equal(human) {
			t.Fatalf("occurred %v %v", occurred, err)
		}
	})
	t.Run("views and quiet", func(t *testing.T) {
		e := newEnv(t)
		pid, _, msgID := e.published()
		e.max.SetViews(msgID, 77)
		e.reconcile()
		pub := e.publication(pid)
		if e.placement(pid).Status != domain.StatusPublished || pub.Views == nil || *pub.Views != 77 {
			t.Fatalf("views %+v", pub)
		}
		// Через 5 мин просмотры не перечитываются: прошло меньше часа.
		e.max.SetViews(msgID, 90)
		e.reconcile()
		if v := *e.publication(pid).Views; v != 77 {
			t.Fatalf("views refreshed too often: %d", v)
		}
	})
	t.Run("max error retries without violation", func(t *testing.T) {
		e := newEnv(t)
		pid, _, _ := e.published()
		e.max.FailWith(errors.New("max: status 502"))
		e.reconcile()
		e.max.FailWith(nil)
		if e.placement(pid).Status != domain.StatusPublished || e.count(`SELECT count(*) FROM violation`) != 0 {
			t.Fatal("violation on MAX failure")
		}
	})
}

func TestReconcileRecreatesMissingPublishJob(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.scheduledNow(0)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE job SET status = 'cancelled' WHERE dedupe_key = $1`, domain.PublishKey(pid)); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(6 * time.Minute)
	if err := e.svc.ReconcilePublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.jobStatus(domain.PublishKey(pid)) != "pending" || len(e.logged("publish job missing, recreated")) != 1 {
		t.Fatal("publish job not recreated")
	}
	e.runJobs()
	if p := e.placement(pid); p.Status != domain.StatusPublished {
		t.Fatalf("after recreate: %s", p.Status)
	}
}
