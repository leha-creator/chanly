package postgres_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func preparedWorkflow(t *testing.T, e env) (domain.Workflow, domain.Payout) {
	t.Helper()
	ctx := context.Background()
	p, c := e.fixture(t)
	po := e.payout(t, p, c)
	if err := e.store.InsertPayout(ctx, po); err != nil {
		t.Fatal(err)
	}
	scope := domain.ProviderScope{Environment: "local-emulator", Account: "synthetic-account"}
	if _, err := e.store.BindExecutor(ctx, domain.ExecutorBinding{Scope: scope, OrganizationID: p.BloggerID, ExecutorID: 7, LookupKind: "inn", LookupValue: "000000000000", EmploymentKind: "selfemployed", VerifiedAt: t0}); err != nil {
		t.Fatal(err)
	}
	w, err := e.store.PrepareWorkflow(ctx, domain.Workflow{PayoutID: po.ID, Scope: scope, CreatedAt: t0, Terms: domain.AgreementTerms{Amount: po.Amount, Currency: "RUB", ExecutorID: 7, EmploymentKind: "selfemployed", Title: "synthetic", Description: "synthetic", SignatureForm: "manual", DocumentNumber: "synthetic-1", DocumentDate: "2026-11-09"}})
	if err != nil {
		t.Fatal(err)
	}
	return w, po
}

func TestWorkflowSnapshotAndAtomicClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, po := preparedWorkflow(t, e)
	if _, err := e.store.PrepareWorkflow(ctx, w); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	changed := w
	changed.Terms.Amount++
	if _, err := e.store.PrepareWorkflow(ctx, changed); err == nil {
		t.Fatal("changed terms accepted")
	}
	if _, err := e.d.Pool.Exec(ctx, "UPDATE rocketwork_workflow SET terms='{}' WHERE payout_id=$1", w.PayoutID); err == nil {
		t.Fatal("mutable snapshot")
	}
	o, err := e.store.PrepareOperation(ctx, domain.WorkflowOperation{PayoutID: w.PayoutID, Action: "create", PayloadHash: w.TermsHash, CorrelationID: ids.New(), UpdatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := e.store.TransitionOperation(ctx, o, domain.OperationInFlight)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	o.State = domain.OperationInFlight
	if ok, err := e.store.TransitionOperation(ctx, o, domain.OperationUncertain); err != nil || !ok {
		t.Fatalf("unknown: %v", err)
	}
	reloaded, err := e.store.PrepareOperation(ctx, o)
	if err != nil || reloaded.State != domain.OperationUncertain || reloaded.CorrelationID != o.CorrelationID {
		t.Fatalf("lost checkpoint: %v", err)
	}
	if _, err := e.d.Pool.Exec(ctx, "UPDATE rocketwork_operation SET state='prepared' WHERE payout_id=$1", w.PayoutID); err == nil {
		t.Fatal("database allowed replay")
	}
	// Even an old code path marking failed cannot release a new payout key.
	if _, err := po.MarkFailed(domain.FailureProviderError, "synthetic", t0); err != nil {
		t.Fatal(err)
	}
	if err := e.store.UpdatePayout(ctx, &po); err != nil {
		t.Fatal(err)
	}
	next, err := po.NextAttempt(ids.New(), po.ContractorID, ids.New(), t0)
	if err != nil {
		t.Fatal(err)
	}
	next.CreatedBy = po.CreatedBy
	if err := e.store.InsertPayout(ctx, next); !errors.Is(err, domain.ErrPayoutExists) {
		t.Fatalf("second payout: %v", err)
	}
}

func TestBindingCannotMoveBetweenOrganizationsOrAccounts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, po := preparedWorkflow(t, e)
	b, err := e.store.ExecutorBinding(ctx, w.Scope, po.BloggerOrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	b.ExecutorID = 8
	if _, err := e.store.BindExecutor(ctx, b); err == nil {
		t.Fatal("silently rebound executor")
	}
	b.ExecutorID = 7
	b.Scope.Account = "different-account"
	if _, err := e.store.BindExecutor(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.ExecutorBinding(ctx, domain.ProviderScope{Environment: "production", Account: w.Scope.Account}, po.BloggerOrganizationID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("cross-environment binding")
	}
}

func TestWorkflowMigrationRoundTripAndLegacyGuard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	migrate := func(command string) error {
		return db.MigrateURL(ctx, e.d.OwnerURL, dbtest.AppRole, command, logx.Discard())
	}
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // withdrawal 21
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // events 20
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // workflow 18, after onboarding 19
	p, c := e.fixture(t)
	po := e.payout(t, p, c)
	po.ExternalID = "p-old-fake-id"
	if err := e.store.InsertPayout(ctx, po); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db.MigrateUp); err != nil {
		t.Fatal(err)
	}
	w, err := e.store.Workflow(ctx, po.ID, false)
	if err != nil || w.Phase != "legacy" || w.TaskID != nil || w.Scope.Environment != "legacy" {
		t.Fatalf("legacy converted to live: %+v %v", w, err)
	}
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // empty withdrawal 21
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // empty events 20
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	} // empty onboarding 19
	if err := migrate(db.MigrateDown); err == nil {
		t.Fatal("rollback discarded history")
	}
	if _, err := e.store.Workflow(ctx, po.ID, false); err != nil {
		t.Fatal("failed rollback lost checkpoint")
	}
}
