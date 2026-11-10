package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type withdrawalProvider struct{ *taskProvider }

func (p withdrawalProvider) CreateWithdrawal(ctx context.Context, external string, t domain.WithdrawalTerms) (domain.ProviderTask, error) {
	p.check(ctx)
	p.creates++
	p.task = domain.ProviderTask{ID: 99, ExternalID: external, Type: "Withdrawal", Status: "paying", Amount: t.Amount, ExecutorID: t.Destination.ExecutorID, ExecutorINN: t.Destination.CompanyINN, EmploymentKind: "self"}
	return domain.ProviderTask{}, errors.New("response lost")
}

func TestWithdrawalRecoveryKeepsOneTransferAndOneLedgerTransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := withdrawalProvider{&taskProvider{t: t, actions: map[string]int{}}}
	e.svc.Provider = p
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	dest := domain.WithdrawalDestination{CompanyINN: "0000000000", BIC: "000000000", AccountNumber: "00000000000000000000", ExecutorID: 7}
	e.svc.Config.WithdrawalDestination = &dest
	id := uuid.New()
	terms := domain.WithdrawalTerms{Destination: dest, Amount: 10000, MaxCommission: 100, AdminID: e.admin, ApproverID: ledgertest.User(t, e.d.Pool), Reason: "synthetic own funds withdrawal"}
	if _, err := e.svc.PrepareWithdrawal(ctx, id, terms); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.svc.ReconcileWithdrawal(ctx, id); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if p.creates != 1 {
		t.Fatal("unknown withdrawal repeated")
	}
	changed := terms
	changed.Amount++
	if _, err := e.svc.PrepareWithdrawal(ctx, id, changed); !errors.Is(err, domain.ErrInvalidPayout) {
		t.Fatal("approval snapshot replaced")
	}
	p.visible = true
	if err := e.svc.ReconcileWithdrawal(ctx, id); !isPostponed(err) {
		t.Fatal(err)
	}
	p.task.Status = "paid"
	fee := money.Kopecks(10)
	p.task.Commission = &fee
	for i := 0; i < 2; i++ {
		if err := e.svc.ReconcileWithdrawal(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if p.creates != 1 || e.count("SELECT count(*) FROM ledger_transaction WHERE idempotency_key=$1", ledgerdomain.OperationKey(ledgerdomain.KindManual, "rocketwork-withdrawal:"+id.String())) != 1 {
		t.Fatal("duplicate transfer or ledger transaction")
	}
}

func TestReturnEvidenceIsIdempotentAndDoesNotRewritePaidPayout(t *testing.T) {
	e, provider, p := workflowEnv(t)
	ctx := context.Background()
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	fee, amount := money.Kopecks(10), p.Amount
	provider.task.Status, provider.task.Commission, provider.task.ExecutorAmount = "paid", &fee, &amount
	if err := e.svc.DrivePayout(ctx, p.ID); !isPostponed(err) {
		t.Fatal(err)
	}
	evidence := domain.ReturnEvidence{ID: uuid.New(), PayoutID: p.ID, DocumentID: uuid.New(), AdminID: e.admin, ApproverID: ledgertest.User(t, e.d.Pool), Amount: p.Amount, Destination: "rocketwork_deposit", Reason: "bank receipt verified by two operators", ReceivedAt: start}
	for i := 0; i < 2; i++ {
		if err := e.svc.RecordPayoutReturn(ctx, evidence); err != nil {
			t.Fatal(err)
		}
	}
	if e.payout(p.ID).Status != domain.StatusSucceeded || e.payable(p.BloggerOrganizationID) != p.Amount {
		t.Fatal("return did not restore liability while preserving history")
	}
	if e.count("SELECT count(*) FROM ledger_transaction WHERE idempotency_key=$1", ledgerdomain.OperationKey(ledgerdomain.KindManual, "payout-return:"+evidence.ID.String())) != 1 {
		t.Fatal("duplicate correction")
	}
	evidence.ID, evidence.DocumentID = uuid.New(), uuid.New()
	if err := e.svc.RecordPayoutReturn(ctx, evidence); !errors.Is(err, domain.ErrInvalidPayout) {
		t.Fatal("return exceeded original payout")
	}
	if !errors.Is(e.svc.ReversePayout(ctx, p.ID), domain.ErrUnsupported) || !errors.Is(e.svc.AnnulReceipt(ctx, p.ID), domain.ErrUnsupported) {
		t.Fatal("unsupported remote reversal accepted")
	}
}
