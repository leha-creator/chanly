package app_test

import (
	"context"
	"testing"

	"github.com/leha-creator/chanly/internal/payout/domain"
)

func TestCancellationStopsForwardActionsAndDoesNotReverseLedger(t *testing.T) {
	e, provider, p := workflowEnv(t)
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if err := e.svc.RequestCancellation(ctx, p.ID, e.admin, "operator cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if len(provider.actions) != 0 {
		t.Fatal("missing signature evidence allowed cancel or pay")
	}
	unsigned := false
	provider.task.CustomerSigned, provider.task.ExecutorSigned = &unsigned, &unsigned
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if provider.actions["cancel"] != 1 || provider.actions["pay"] != 0 || e.payout(p.ID).Status != domain.StatusFailed {
		t.Fatal("cancel did not reconcile once")
	}
	if e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 0 {
		t.Fatal("cancel posted money")
	}
}
