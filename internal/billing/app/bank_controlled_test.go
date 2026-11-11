package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func controlledBank(e *env) {
	setupBank(e)
	e.svc.BankBudget = postgres.New(e.d.Pool, logx.Discard())
	e.svc.BankConfig.Access = domain.BankControlled
	e.svc.BankConfig.CreditPolicy = domain.BankCreditPolicy{RunID: uuid.New(), OrganizationID: e.org, Account: platformAccount, Origin: "https://business.tbank.ru/openapi", MaxPayment: 10000, MaxTotal: 15000, ExpiresAt: e.clk.Now().Add(time.Hour)}
}
func (e *env) bankBudgetSpent() int64 {
	e.t.Helper()
	var spent int64
	if err := e.d.Pool.QueryRow(context.Background(), "SELECT spent FROM bank_credit_policy WHERE run_id=$1", e.svc.BankConfig.CreditPolicy.RunID).Scan(&spent); err != nil {
		e.t.Fatal(err)
	}
	return spent
}

func TestControlledBankCreditBudgetAndManualGate(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	first := e.invoice(10000)
	second := e.invoice(6000)
	result := e.importStatement(statementFile(platformAccount, "controlled", payment{1, 10000, "7707083893", "Оплата по счету " + first.Number + " " + e.svc.BankConfig.CreditPolicy.Marker()}, payment{2, 6000, "7707083893", "Оплата по счету " + second.Number + " " + e.svc.BankConfig.CreditPolicy.Marker()}))
	if result.Matched != 1 || result.Unmatched != 1 || e.balance(e.org) != 10000 || e.bankBudgetSpent() != 10000 {
		t.Fatalf("controlled result %+v", result)
	}
	if status, paid := e.invoiceStatus(second.ID); status != domain.InvoiceIssued || paid != nil {
		t.Fatal("denied payment marked invoice paid")
	}
	if _, err := e.d.Pool.Exec(context.Background(), "UPDATE bank_credit_policy SET spent=0 WHERE run_id=$1", e.svc.BankConfig.CreditPolicy.RunID); err == nil {
		t.Fatal("budget reset allowed")
	}
	if _, err := e.d.Pool.Exec(context.Background(), "UPDATE bank_credit_policy SET max_total=max_total+1 WHERE run_id=$1", e.svc.BankConfig.CreditPolicy.RunID); err == nil {
		t.Fatal("policy mutation allowed")
	}
	if _, err := e.d.Pool.Exec(context.Background(), "DELETE FROM bank_credit_spend"); err == nil {
		t.Fatal("spend history deletion allowed")
	}
	id := e.unmatchedID()
	match := func() {
		t.Helper()
		_, err := e.svc.MatchManually(context.Background(), id, e.org, second.ID, e.admin, "controlled test")
		if !errors.Is(err, domain.ErrBankCreditRestricted) {
			t.Fatalf("manual bypass: %v", err)
		}
	}
	match()
	// Reconstructing the adapter (worker restart) cannot replenish the budget.
	e.svc.BankBudget = postgres.New(e.d.Pool, logx.Discard())
	match()
	e.svc.BankConfig.CreditPolicy.MaxTotal = 20000
	match() // same run may not change terms.
	e.svc.BankConfig.CreditPolicy.MaxTotal = 15000
	e.clk.Advance(2 * time.Hour)
	match()
	if e.balance(e.org) != 10000 || e.bankBudgetSpent() != 10000 {
		t.Fatal("restricted credits changed money")
	}
}

func TestControlledBankCreditRollback(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	e.svc.BankConfig.CreditPolicy.MaxTotal = 20000
	first := e.invoice(6000)
	second := e.invoice(6000)
	notices := &failingNotice{previous: e.svc.Notifier}
	e.svc.Notifier = notices
	file := statementFile(platformAccount, "controlled rollback", payment{1, 6000, "7707083893", "Оплата по счету " + first.Number + " " + e.svc.BankConfig.CreditPolicy.Marker()}, payment{2, 6000, "7707083893", "Оплата по счету " + second.Number + " " + e.svc.BankConfig.CreditPolicy.Marker()})
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, file); err == nil {
		t.Fatal("expected notification failure")
	}
	if e.bankBudgetSpent() != 6000 || e.balance(e.org) != 6000 {
		t.Fatal("failed deposit consumed budget")
	}
	e.svc.Notifier = notices.previous
	res, err := e.svc.ImportStatement(context.Background(), e.admin, file)
	if err != nil || res.Duplicates != 1 || res.Matched != 1 {
		t.Fatalf("resume %+v %v", res, err)
	}
	if e.bankBudgetSpent() != 12000 || e.balance(e.org) != 12000 {
		t.Fatal("wrong resumed budget")
	}
}

func TestControlledBankCreditConcurrentTotal(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	e.svc.BankConfig.Access = domain.BankAutomation // prepare unmatched fixtures before admission.
	purpose := "проверка " + e.svc.BankConfig.CreditPolicy.Marker()
	e.importStatement(statementFile(platformAccount, "unmatched", payment{1, 7000, "7707083893", purpose}, payment{2, 7000, "7707083893", purpose}))
	page, err := e.svc.ListBankTransactions(context.Background(), domain.MatchUnmatched, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("unmatched %+v %v", page, err)
	}
	e.svc.BankConfig.Access = domain.BankControlled
	e.svc.BankConfig.CreditPolicy.MaxTotal = 10000
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range page.Items {
		wg.Go(func() {
			_, err := e.svc.MatchManually(context.Background(), item.ID, e.org, uuid.Nil, e.admin, "controlled concurrent test")
			outcomes <- err
		})
	}
	wg.Wait()
	close(outcomes)
	success, denied := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrBankCreditRestricted) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 || e.bankBudgetSpent() != 7000 || e.balance(e.org) != 7000 {
		t.Fatalf("concurrent admission success=%d denied=%d", success, denied)
	}
}

func TestControlledBankCreditRejectsWrongScopeAndLargeAmount(t *testing.T) {
	for _, reason := range []string{"organization", "amount", "expired", "marker"} {
		t.Run(reason, func(t *testing.T) {
			e := newEnv(t)
			controlledBank(e)
			inv := e.invoice(10000)
			switch reason {
			case "organization":
				e.svc.BankConfig.CreditPolicy.OrganizationID = uuid.New()
			case "amount":
				e.svc.BankConfig.CreditPolicy.MaxPayment = 9999
			case "expired":
				e.svc.BankConfig.CreditPolicy.ExpiresAt = e.clk.Now()
			}
			purpose := "Оплата по счету " + inv.Number + " " + e.svc.BankConfig.CreditPolicy.Marker()
			if reason == "marker" {
				purpose = "Оплата по счету " + inv.Number
			}
			res := e.importStatement(statementFile(platformAccount, "scope", payment{1, 10000, "7707083893", purpose}))
			if res.Matched != 0 || res.Unmatched != 1 || e.balance(e.org) != 0 || e.count("SELECT count(*) FROM bank_credit_spend") != 0 {
				t.Fatalf("scope admitted %+v", res)
			}
		})
	}
}

func TestBankReadonlyRefusesSyncAndManualAPICredit(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	e.svc.BankConfig.Access = domain.BankReadOnly
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		t.Fatal("readonly started synchronization")
		return domain.BankPage{}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); !errors.Is(err, domain.ErrBankCreditRestricted) {
		t.Fatalf("readonly sync %v", err)
	}
	if err := e.svc.PrepareBankReplay(context.Background(), start.Add(-time.Hour), start); !errors.Is(err, domain.ErrBankCreditRestricted) {
		t.Fatalf("readonly replay %v", err)
	}
	st := domain.BankStatement{ID: uuid.New(), Source: "tbank", Account: platformAccount, CreatedAt: e.clk.Now(), UpdatedAt: e.clk.Now()}
	bt := domain.BankTransaction{ID: uuid.New(), StatementID: st.ID, Incoming: domain.Incoming{ExternalID: "readonly", OperationDate: start, Amount: 10000}, MatchStatus: domain.MatchUnmatched, CreatedAt: start, UpdatedAt: start}
	err := e.svc.Tx.InTx(context.Background(), func(ctx context.Context) error {
		if err := e.svc.Statements.Insert(ctx, st); err != nil {
			return err
		}
		_, err := e.svc.BankTransactions.InsertIfNew(ctx, bt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MatchManually(context.Background(), bt.ID, e.org, uuid.Nil, e.admin, "readonly test"); !errors.Is(err, domain.ErrBankCreditRestricted) {
		t.Fatalf("readonly manual %v", err)
	}
	if e.balance(e.org) != 0 {
		t.Fatal("readonly changed money")
	}
}
