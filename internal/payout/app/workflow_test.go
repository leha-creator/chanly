package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/payout/domain"
	payoutpg "github.com/leha-creator/chanly/internal/payout/postgres"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

type taskProvider struct {
	domain.Provider
	mu                  sync.Mutex
	t                   *testing.T
	task                domain.ProviderTask
	creates             int
	actions             map[string]int
	loseCreate, visible bool
}

func (p *taskProvider) check(ctx context.Context) {
	p.t.Helper()
	if _, ok := tx.FromContext(ctx); ok {
		p.t.Error("network within database transaction")
	}
}
func (p *taskProvider) ResolveExecutor(ctx context.Context, r domain.ExecutorLookup) (domain.ExecutorResolution, error) {
	p.check(ctx)
	return domain.ExecutorResolution{ExecutorID: 7, Status: domain.ContractorActive}, nil
}
func (p *taskProvider) GetDepositBalance(ctx context.Context) (money.Kopecks, error) {
	p.check(ctx)
	return 1_000_000_000, nil
}
func (p *taskProvider) CreateTask(ctx context.Context, o domain.TaskOrder) (domain.ProviderTask, error) {
	p.check(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	act, tax := o.Terms.ActRequired, o.Terms.TaxRetention
	p.task = domain.ProviderTask{ID: 77, ExternalID: o.ExternalID, Type: "OneTime", Status: "inwork", Amount: o.Terms.Amount, ExecutorID: 7, ExecutorINN: o.ExecutorINN, EmploymentKind: o.Terms.EmploymentKind, ActRequired: &act, TaxRetention: &tax, SignatureForm: o.Terms.SignatureForm}
	if p.loseCreate {
		return domain.ProviderTask{}, errors.New("lost create response")
	}
	return p.task, nil
}
func (p *taskProvider) ReadTask(ctx context.Context, id int64) (domain.ProviderTask, error) {
	p.check(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.task, nil
}
func (p *taskProvider) FindTask(ctx context.Context, external string) (domain.ProviderTask, bool, error) {
	p.check(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.task, p.visible && p.task.ExternalID == external, nil
}
func (p *taskProvider) ActOnTask(ctx context.Context, id int64, action string) (domain.ProviderTask, error) {
	p.check(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions[action]++
	p.task.Status = map[string]string{"complete": "accepting", "sign_act": "completed", "pay": "paying", "cancel": "canceled"}[action]
	return p.task, nil
}

func workflowEnv(t *testing.T) (*env, *taskProvider, domain.Payout) {
	e := newEnv(t)
	org := e.blogger(accountdomain.SelfEmployed)
	_, id := e.accepted(org, 10000)
	p := &taskProvider{t: t, actions: map[string]int{}, visible: true}
	e.svc.Provider = p
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	e.svc.Config.AgreementTemplate = &domain.AgreementTerms{Currency: "RUB", Title: "Synthetic placement", Description: "Synthetic service", SignatureForm: "manual", DocumentNumber: "synthetic-1", DocumentDate: "2026-11-09", ActRequired: true, TaxRetention: false, MaxCommission: 100}
	return e, p, e.payout(id)
}

type payoutActSource func(context.Context, domain.Payout) (domain.ActData, error)

func (f payoutActSource) ForAct(ctx context.Context, p domain.Payout) (domain.ActData, error) {
	return f(ctx, p)
}

func TestWorkflowFreezesConfiguredActBeforeUnknownCreate(t *testing.T) {
	e, provider, p := workflowEnv(t)
	e.svc.Config.ActTemplates = &domain.ActTemplates{SelfEmployed: "Акт СМЗ", Entrepreneur: "Акт ИП", EntrepreneurNPD: "Акт ИП на НПД"}
	e.svc.Config.AgreementTemplate.TaxRetention = true
	reads := 0
	e.svc.ActSource = payoutActSource(func(ctx context.Context, po domain.Payout) (domain.ActData, error) {
		reads++
		return domain.ActData{OrganizationID: po.BloggerOrganizationID, LegalType: domain.SelfEmployed, TaxRegime: "npd", OfferAcceptedAt: po.CreatedAt.Add(-72 * time.Hour), OrderNumber: 77, PublicationURL: "https://max.ru/published/77", TopHours: 1, DurationHours: 24, PublishedAt: po.CreatedAt.Add(-25 * time.Hour), RemovedAt: po.CreatedAt.Add(-time.Hour)}, nil
	})
	provider.loseCreate = true
	provider.visible = false
	if err := e.svc.Send(context.Background(), p.ID, 1); !isPostponed(err) {
		t.Fatal(err)
	}
	store := payoutpg.New(e.d.Pool, logx.Discard())
	before, err := store.Workflow(context.Background(), p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Terms.Variables == nil || before.Terms.DocumentName != "Акт СМЗ" || !before.Terms.TaxRetention || before.Terms.Variables.Table[0].OrderNumber != "77" {
		t.Fatalf("act was not persisted: %+v", before.Terms)
	}
	e.svc.Config.ActTemplates.SelfEmployed = "changed-template"
	e.svc.ActSource = payoutActSource(func(context.Context, domain.Payout) (domain.ActData, error) {
		t.Fatal("retry reread mutable act facts")
		return domain.ActData{}, nil
	})
	if err = e.svc.Send(context.Background(), p.ID, 2); !isPostponed(err) {
		t.Fatal(err)
	}
	after, err := store.Workflow(context.Background(), p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if before.TermsHash != after.TermsHash || after.Terms.DocumentName != "Акт СМЗ" || reads != 1 || provider.creates != 1 {
		t.Fatal("unknown create changed act or caused duplicate payout")
	}
}

func TestWorkflowUnknownCreateRetryRetainsIdentity(t *testing.T) {
	e, provider, p := workflowEnv(t)
	ctx := context.Background()
	provider.loseCreate = true
	provider.visible = false
	if err := e.svc.Send(ctx, p.ID, 1); !isPostponed(err) {
		t.Fatal(err)
	}
	e.svc.SendExhausted(ctx, p.ID, errors.New("attempts exhausted"))
	if got := e.payout(p.ID); got.Status == domain.StatusFailed || !got.ReconciliationRequired {
		t.Fatal("uncertain payout made retryable")
	}
	if next, err := e.svc.Retry(ctx, p.ID, e.admin, "read reconciliation"); err != nil || next.ID != p.ID {
		t.Fatalf("retry replaced identity: %v", err)
	}
	if err := e.svc.Send(ctx, p.ID, 2); !isPostponed(err) {
		t.Fatal(err)
	}
	if provider.creates != 1 {
		t.Fatal("empty lookup authorized another POST")
	}
	provider.visible = true
	if err := e.svc.Send(ctx, p.ID, 3); !isPostponed(err) {
		t.Fatal(err)
	}
	if got := e.payout(p.ID); got.ExternalID != "77" || got.Status != domain.StatusSent || provider.creates != 1 {
		t.Fatal("recovery did not attach original task")
	}
}

func TestWorkflowConcurrentWorkersPayOnlyOnce(t *testing.T) {
	e, provider, p := workflowEnv(t)
	ctx := context.Background()
	for tick := 0; tick < 5; tick++ {
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) && !errors.Is(err, domain.ErrConcurrentUpdate) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	if provider.creates != 1 || provider.actions["complete"] != 1 || provider.actions["sign_act"] != 1 || provider.actions["pay"] != 1 {
		t.Fatalf("writes: create=%d actions=%v", provider.creates, provider.actions)
	}
	fee := money.Kopecks(10)
	amount := p.Amount
	provider.task.Status = "paid"
	provider.task.Commission = &fee
	provider.task.ExecutorAmount = &amount
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if got := e.payout(p.ID); got.Status != domain.StatusSucceeded || got.ProviderFee != fee {
		t.Fatal("not paid")
	}
	if n := e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID); n != 1 {
		t.Fatalf("ledger count=%d", n)
	}
	provider.task.ReceiptURL = "https://receipts.example.test/late"
	if err := e.svc.DrivePayout(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.payout(p.ID); got.ReceiptURL != provider.task.ReceiptURL {
		t.Fatal("late receipt lost")
	}
	provider.task.Status = "error"
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	if got := e.payout(p.ID); got.Status != domain.StatusSucceeded {
		t.Fatal("stale error reverted ledger outcome")
	}
	if provider.actions["pay"] != 1 || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 1 {
		t.Fatal("late document or stale event repeated payment")
	}
}

func TestPaidWithoutFinalFinancialDetailsNeverPaysAgain(t *testing.T) {
	e, provider, p := workflowEnv(t)
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	provider.task.Status = "paid"
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if provider.actions["pay"] != 0 || provider.creates != 1 || e.payout(p.ID).Status == domain.StatusFailed {
		t.Fatal("missing financial details authorized another payment")
	}
	fee := money.Kopecks(101)
	amount := p.Amount
	provider.task.Commission = &fee
	provider.task.ExecutorAmount = &amount
	if err := e.svc.DrivePayout(ctx, p.ID); !errors.Is(err, domain.ErrInvalidPayout) {
		t.Fatal("fee limit ignored")
	}
	if n := e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID); n != 0 {
		t.Fatal("unreconciled fee posted")
	}
}

func TestWorkflowUnlimitedCommissionRecordsActualFeeOnce(t *testing.T) {
	e, provider, p := workflowEnv(t)
	e.svc.Config.AgreementTemplate.MaxCommission = 0
	e.svc.Config.AgreementTemplate.CommissionUnlimited = true
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	fee, amount := money.Kopecks(500), p.Amount
	provider.task.Status = "paid"
	provider.task.Commission = &fee
	provider.task.ExecutorAmount = &amount
	for i := 0; i < 2; i++ {
		if err := e.svc.DrivePayout(ctx, p.ID); err != nil && !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if got := e.payout(p.ID); got.Status != domain.StatusSucceeded || got.ProviderFee != fee {
		t.Fatal("actual provider commission was not recorded")
	}
	if provider.creates != 1 || provider.actions["pay"] != 0 || e.count("SELECT count(*) FROM ledger_transaction WHERE payout_id=$1", p.ID) != 1 {
		t.Fatal("reconciliation duplicated a provider write or ledger entry")
	}
}
