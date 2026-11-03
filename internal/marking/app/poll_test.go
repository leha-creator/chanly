package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
)

// pollRunAt — время следующего опроса объекта.
func (e *env) pollRunAt(ref domain.ObjectRef) time.Time {
	e.t.Helper()
	var at time.Time
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT run_at FROM job WHERE dedupe_key = $1 AND status = 'pending'`,
		app.PollDedupeKey(ref)).Scan(&at); err != nil {
		e.t.Fatalf("poll job of %s: %v", ref, err)
	}
	return at.UTC()
}

func TestPollSchedule(t *testing.T) {
	e := newEnv(t)
	e.ord.HoldStatus(e.platform.String(), true)
	e.ord.HoldStatus(e.adv.String(), true)
	e.initialChain(e.adv)
	e.run()
	ref := e.org(e.platform).Ref()
	sentAt := e.clk.Now()
	if got := e.pollRunAt(ref); !got.Equal(sentAt.Add(time.Minute)) {
		t.Fatalf("first poll at %s, want +1m", got)
	}
	// Первые 10 минут — раз в минуту.
	for i := 1; i < 10; i++ {
		e.clk.Set(e.pollRunAt(ref))
		e.run()
		if got := e.pollRunAt(ref); got.Sub(e.clk.Now()) != time.Minute {
			t.Fatalf("poll %d: next in %s, want 1m", i, got.Sub(e.clk.Now()))
		}
	}
	// Затем — раз в 10 минут.
	e.clk.Set(e.pollRunAt(ref))
	e.run()
	if got := e.pollRunAt(ref); got.Sub(e.clk.Now()) != 10*time.Minute {
		t.Fatalf("after 10 minutes: next in %s, want 10m", got.Sub(e.clk.Now()))
	}
	if e.count(`SELECT max(attempts) FROM job WHERE kind = 'poll_ord_status'`) != 0 {
		t.Fatal("polls counted as attempts")
	}

	// Ошибка сети при опросе — следующий опрос по тому же расписанию.
	e.ord.FailStatus(errors.New("connection reset"))
	e.clk.Set(e.pollRunAt(ref))
	e.run()
	if got := e.pollRunAt(ref); got.Sub(e.clk.Now()) != 10*time.Minute || e.org(e.platform).Sync.Status != domain.SyncSent {
		t.Fatalf("after poll error: next in %s, status %s", got.Sub(e.clk.Now()), e.org(e.platform).Sync.Status)
	}
	if len(e.logged("ord status poll failed")) != 2 { // платформа и рекламодатель
		t.Fatalf("poll failures logged %d times", len(e.logged("ord status poll failed")))
	}
	e.ord.FailStatus(nil)

	// После простоя опрос выполняется один раз.
	calls := e.ord.Calls(ord.OpStatus)
	e.clk.Advance(3 * time.Hour)
	e.run()
	if got := e.ord.Calls(ord.OpStatus) - calls; got != 2 {
		t.Fatalf("%d polls after downtime, want 2", got)
	}

	// 24 часа без финального статуса — status_timeout и оповещение.
	for e.clk.Now().Before(sentAt.Add(24 * time.Hour)) {
		e.clk.Set(e.pollRunAt(ref))
		e.run()
	}
	o := e.org(e.platform)
	if o.Sync.Status != domain.SyncError || o.Sync.Error != domain.ReasonStatusTimeout {
		t.Fatalf("after 24h: %+v", o.Sync)
	}
	if !e.alerts.has("ord_sync_failed:organization") || len(e.logged("ord status timeout")) == 0 {
		t.Fatal("status timeout not alerted or logged")
	}
}

func TestErirErrorStopsDependants(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.ord.RejectObject(e.adv.String(), domain.OrdErirAsyncError, "ИНН не найден в ЕГРЮЛ")
	e.approve(o.Version.ID)
	e.settle(8)
	adv := e.org(e.adv)
	if adv.Sync.Status != domain.SyncError || adv.Sync.OrdStatus != domain.OrdErirAsyncError || adv.Sync.Error != "ИНН не найден в ЕГРЮЛ" {
		t.Fatalf("advertiser: %+v", adv.Sync)
	}
	if !e.alerts.has("ord_sync_failed:organization") {
		t.Fatal("no alert on ERIR error")
	}
	if e.ord.Calls(ord.OpContract) != 0 || e.ord.Calls(ord.OpCreative) != 0 {
		t.Fatal("dependants of a failed organization were sent")
	}
	// Платформа доходит до ERIR async success.
	e.settle(4)
	if p := e.org(e.platform); p.Sync.Status != domain.SyncSynced || p.Sync.OrdStatus != domain.OrdErirAsyncSuccess {
		t.Fatalf("platform: %+v", p.Sync)
	}
	if len(e.logged("ord object rejected")) == 0 {
		t.Fatal("rejection not logged")
	}
}
