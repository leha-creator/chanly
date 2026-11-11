package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/domain"
)

func controlledBank(e *env) {
	setupBank(e)
	e.svc.BankConfig.Access = domain.BankControlled
	e.svc.BankConfig.DevRealEnabled = true
	e.svc.BankConfig.DevPaymentCode = "test-server"
}
func TestDevBankCreditHasNoRunBudgetOrExpiry(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	first := e.invoice(100000)
	second := e.invoice(200000)
	e.clk.Advance(48 * time.Hour)
	file := statementFile(platformAccount, "dev payments", payment{1, 100000, "7707083893", "Оплата по счету " + first.Number + " " + e.svc.BankConfig.PaymentMarker()}, payment{2, 200000, "7707083893", "Оплата по счету " + second.Number + " " + e.svc.BankConfig.PaymentMarker()})
	res := e.importStatement(file)
	if res.Matched != 2 || e.balance(e.org) != 300000 {
		t.Fatalf("dev credits %+v", res)
	}
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, file); err == nil {
		t.Fatal("duplicate file accepted")
	}
	if e.balance(e.org) != 300000 || e.count("SELECT count(*) FROM bank_credit_policy") != 0 {
		t.Fatal("duplicate credit or obsolete budget used")
	}
}
func TestDevBankCreditMarkerAndFlagGateManualMatch(t *testing.T) {
	for _, reason := range []string{"flag", "empty", "wrong", "prefix"} {
		t.Run(reason, func(t *testing.T) {
			e := newEnv(t)
			controlledBank(e)
			inv := e.invoice(10000)
			marker := e.svc.BankConfig.PaymentMarker()
			switch reason {
			case "flag":
				e.svc.BankConfig.DevRealEnabled = false
			case "empty":
				marker = ""
			case "wrong":
				marker = "DEV-TBANK-OTHER"
			case "prefix":
				marker += "-OTHER"
			}
			res := e.importStatement(statementFile(platformAccount, "gate", payment{1, 10000, "7707083893", "Оплата по счету " + inv.Number + " " + marker}))
			if res.Unmatched != 1 || e.balance(e.org) != 0 {
				t.Fatalf("gate %+v", res)
			}
			_, err := e.svc.MatchManually(context.Background(), e.unmatchedID(), e.org, inv.ID, e.admin, "dev check")
			if !errors.Is(err, domain.ErrBankCreditRestricted) {
				t.Fatalf("manual bypass: %v", err)
			}
		})
	}
}
func TestDevBankCreditRollbackAndResume(t *testing.T) {
	e := newEnv(t)
	controlledBank(e)
	first := e.invoice(6000)
	second := e.invoice(6000)
	notices := &failingNotice{previous: e.svc.Notifier}
	e.svc.Notifier = notices
	file := statementFile(platformAccount, "dev rollback", payment{1, 6000, "7707083893", "Оплата по счету " + first.Number + " " + e.svc.BankConfig.PaymentMarker()}, payment{2, 6000, "7707083893", "Оплата по счету " + second.Number + " " + e.svc.BankConfig.PaymentMarker()})
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, file); err == nil {
		t.Fatal("expected notice failure")
	}
	if e.balance(e.org) != 6000 {
		t.Fatal("failed deposit committed")
	}
	e.svc.Notifier = notices.previous
	res, err := e.svc.ImportStatement(context.Background(), e.admin, file)
	if err != nil || res.Duplicates != 1 || res.Matched != 1 || e.balance(e.org) != 12000 {
		t.Fatalf("resume %+v %v", res, err)
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
