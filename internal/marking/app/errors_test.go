package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestListErrorsAndRetry(t *testing.T) {
	e := newEnv(t)
	e.initialChain(e.adv)
	e.ord.FailNext(ord.OpContract, http.StatusUnprocessableEntity, "body.date: field required")
	e.settle(3)
	contract := e.contract(domain.ContractInitial, e.adv, e.platform)

	page, err := e.svc.ListErrors(context.Background(), "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref != contract.Ref() || page.Items[0].Attempts != 1 ||
		page.Items[0].ExternalID != contract.ID.String() {
		t.Fatalf("errors page: %+v %v", page, err)
	}

	// «Повторить» без основания — отказ; из synced — 409.
	if err := e.svc.Retry(context.Background(), contract.Ref(), e.advUser, ""); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("retry without reason: %v", err)
	}
	if err := e.svc.Retry(context.Background(), e.org(e.platform).Ref(), e.advUser, "проверка"); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("retry of a non-error object: %v", err)
	}
	if err := e.svc.Retry(context.Background(), contract.Ref(), e.advUser, "исправили дату"); err != nil {
		t.Fatal(err)
	}
	if e.jobStatus(app.SyncDedupeKey(contract.Ref())) != "pending" {
		t.Fatal("retry did not enqueue the sync job")
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'ord.retry' AND actor_type = 'admin' AND reason = 'исправили дату'`) != 1 {
		t.Fatal("retry not recorded in audit log")
	}
	e.settle(4)
	if c := e.contract(domain.ContractInitial, e.adv, e.platform); c.Sync.Status == domain.SyncError || c.Sync.Status == domain.SyncPending {
		t.Fatalf("contract after retry: %+v", c.Sync)
	}
	if page, _ := e.svc.ListErrors(context.Background(), "", 10); len(page.Items) != 0 {
		t.Fatalf("errors after retry: %+v", page.Items)
	}
	if _, err := e.svc.ListErrors(context.Background(), "garbage", 10); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestOverdueEridAndSent(t *testing.T) {
	e := newEnv(t)
	_, ch := e.blogger("Кулинария")
	o := e.order(ch)
	e.ord.HoldStatus(e.platform.String(), true)
	e.approve(o.Version.ID)
	e.run()
	ctx := context.Background()
	// T-11: креатив без erid через 61 минуту найден, через 59 — нет.
	if keys, _ := e.svc.OverdueErid(ctx, e.clk.Now().Add(59*time.Minute).Add(-time.Hour)); len(keys) != 0 {
		t.Fatalf("overdue after 59 minutes: %v", keys)
	}
	keys, err := e.svc.OverdueErid(ctx, e.clk.Now().Add(61*time.Minute).Add(-time.Hour))
	if err != nil || len(keys) != 1 || keys[0] != o.Version.ID.String() {
		t.Fatalf("overdue after 61 minutes: %v %v", keys, err)
	}
	if len(e.logged("post version erid overdue")) != 1 {
		t.Fatal("overdue erid not logged")
	}
	// Объект в sent дольше 24 ч.
	sent, err := e.svc.OverdueSent(ctx, e.clk.Now().Add(time.Second))
	if err != nil || len(sent) == 0 {
		t.Fatalf("sent objects: %v %v", sent, err)
	}
	if sent, _ := e.svc.OverdueSent(ctx, e.clk.Now().Add(-time.Second)); len(sent) != 0 {
		t.Fatalf("sent before threshold: %v", sent)
	}
}
