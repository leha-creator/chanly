package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type pageFunc func(context.Context, domain.BankQuery) (domain.BankPage, error)

func (f pageFunc) ReadPage(c context.Context, q domain.BankQuery) (domain.BankPage, error) {
	return f(c, q)
}

func TestBankMissingAndBalanceMismatch(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	in := bankIncoming(e, "7", "Оплата по счету "+inv.Number, "first")
	e.svc.BankConfig.Start = start.Add(-24 * time.Hour)
	e.svc.BankConfig.Period = 24 * time.Hour
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	opening, closing, credit, debit := money.Kopecks(0), money.Kopecks(50), money.Kopecks(100), money.Kopecks(0)
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{Opening: &opening, Closing: &closing, Credit: &credit, Debit: &debit}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.BankSync.LoadBankProgress(context.Background(), platformAccount)
	if err != nil || p.Missing != 1 || !p.BalanceMismatch {
		t.Fatalf("missing %+v %v", p, err)
	}
	if p.LedgerDifference == nil || *p.LedgerDifference != -499950 {
		t.Fatalf("ledger reconciliation %+v", p.LedgerDifference)
	}
	if e.balance(e.org) != 500000 {
		t.Fatal("missing operation reversed deposit")
	}
}

func TestBankConcurrentAccountLock(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	ctx := context.Background()
	err := e.svc.BankSync.WithBankLock(ctx, platformAccount, func(ctx context.Context) error {
		if err := e.svc.BankSync.WithBankLock(ctx, platformAccount, func(context.Context) error { t.Fatal("concurrent lock acquired"); return nil }); err == nil {
			t.Fatal("second sync accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.BankSync.WithBankLock(ctx, platformAccount, func(context.Context) error { return nil }); err != nil {
		t.Fatal("lock not released")
	}
}

func TestBankRepeatedCursorDoesNotComplete(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{NextCursor: "same"}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err == nil {
		t.Fatal("cycle accepted")
	}
	p, err := e.svc.BankSync.LoadBankProgress(context.Background(), platformAccount)
	if err != nil || p.Cursor != "same" || !p.CompletedTo.IsZero() {
		t.Fatalf("checkpoint %+v %v", p, err)
	}
}

type failingNotice struct {
	calls    int
	previous domain.Notifier
}

func (n *failingNotice) Notify(ctx context.Context, notice domain.Notice) error {
	n.calls++
	if n.calls == 2 {
		return errors.New("notification transaction failure")
	}
	return n.previous.Notify(ctx, notice)
}
func TestSameFileCanResumeAfterPartialCommit(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	one := e.invoice(500000)
	two := e.invoice(500000)
	notice := &failingNotice{previous: e.svc.Notifier}
	e.svc.Notifier = notice
	data := statementFile(platformAccount, "resumable", payment{1, 500000, "7707083893", "Оплата по счету " + one.Number}, payment{2, 500000, "7707083893", "Оплата по счету " + two.Number})
	if _, err := e.svc.ImportStatement(context.Background(), e.admin, data); err == nil {
		t.Fatal("expected partial failure")
	}
	e.svc.Notifier = notice.previous
	result, err := e.svc.ImportStatement(context.Background(), e.admin, data)
	if err != nil || result.Matched != 1 || result.Duplicates != 1 {
		t.Fatalf("resume %+v %v", result, err)
	}
	if e.balance(e.org) != 1000000 || e.count("SELECT count(*) FROM bank_transaction") != 2 {
		t.Fatal("wrong resumed balance")
	}
	st, err := e.svc.Statements.Get(context.Background(), result.StatementID)
	if err != nil || !st.Complete || st.MatchedCount != 2 {
		t.Fatalf("summary %+v %v", st, err)
	}
}

func TestBankIncompleteIdentityIsQuarantined(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	in := bankIncoming(e, "", "Оплата по счету "+inv.Number, "first")
	in.IdentityConflict = true
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 0 || e.count("SELECT count(*) FROM bank_operation_observation WHERE conflict") != 1 {
		t.Fatal("quarantine failed")
	}
}
func setupBank(e *env) {
	store := postgres.New(e.d.Pool, logx.Discard())
	e.svc.BankIdentity = store
	e.svc.BankSync = store
	e.svc.BankConfig = app.BankConfig{Access: domain.BankAutomation, Start: start.Add(-48 * time.Hour), Period: 24 * time.Hour, Overlap: 24 * time.Hour, StaleAfter: time.Hour}
}
func bankIncoming(e *env, number, purpose, id string) domain.Incoming {
	date := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	return domain.Incoming{ExternalID: "tbank:" + platformAccount + ":" + id, BankOperationID: id, DocumentNumber: number, DocDate: &date, ChargeDate: &date, OperationDate: date, Amount: 500000, PayerINN: "7707083893", PayerAccount: "40702810500000000004", Purpose: purpose}
}
func TestBankCrossSourceAndChangedID(t *testing.T) {
	for _, fileFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "file-first", false: "api-first"}[fileFirst], func(t *testing.T) {
			e := newEnv(t)
			setupBank(e)
			inv := e.invoice(500000)
			purpose := "Оплата по счету " + inv.Number
			in := bankIncoming(e, "7", purpose, "first")
			e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
				return domain.BankPage{Incoming: []domain.Incoming{in}, Operations: 1}, nil
			})
			file := statementFile(platformAccount, "bank-test", payment{7, 500000, "7707083893", purpose})
			if fileFirst {
				e.importStatement(file)
			}
			if _, err := e.svc.SyncBank(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !fileFirst {
				res := e.importStatement(file)
				if res.Duplicates != 1 {
					t.Fatalf("file result %+v", res)
				}
			}
			in.ExternalID = "tbank:" + platformAccount + ":changed"
			in.BankOperationID = "changed"
			if _, err := e.svc.SyncBank(context.Background()); err != nil {
				t.Fatal(err)
			}
			if e.balance(e.org) != 500000 {
				t.Fatal("duplicate deposit")
			}
			if e.count("SELECT count(*) FROM bank_transaction") != 1 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
				t.Fatal("duplicate transaction")
			}
		})
	}
}

func TestBankMoscowDocumentDayDeduplicatesAgainstFile(t *testing.T) {
	for _, fileFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "file-first", false: "api-first"}[fileFirst], func(t *testing.T) {
			e := newEnv(t)
			setupBank(e)
			inv := e.invoice(500000)
			purpose := "Оплата по счету " + inv.Number
			in := bankIncoming(e, "7", purpose, "calendar")
			doc := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
			posting := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
			in.DocDate = &doc
			in.ChargeDate = &posting
			in.OperationDate = posting
			e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
				return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
			})
			file := statementFile(platformAccount, "calendar-test", payment{7, 500000, "7707083893", purpose})
			if fileFirst {
				e.importStatement(file)
			}
			if _, err := e.svc.SyncBank(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !fileFirst {
				res := e.importStatement(file)
				if res.Duplicates != 1 {
					t.Fatalf("calendar alias not used: %+v", res)
				}
			}
			if e.balance(e.org) != 500000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 || e.count("SELECT count(*) FROM bank_transaction") != 1 {
				t.Fatal("cross-source duplicate credit")
			}
		})
	}
}

func TestBankCalendarQuarantineCanBeReprocessedOnce(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	in := bankIncoming(e, "7", "Оплата по счету "+inv.Number, "held-calendar")
	doc := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
	in.DocDate = &doc
	paid := time.Date(2026, 10, 18, 22, 45, 57, 0, time.UTC)
	in.ChargeDate = &paid
	in.OperationDate = paid
	in.IdentityConflict = true
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 0 {
		t.Fatal("quarantine credited")
	}
	in.IdentityConflict = false
	for i := 0; i < 2; i++ {
		if _, err := e.svc.SyncBank(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if e.balance(e.org) != 500000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
		t.Fatal("reprocessing did not credit exactly once")
	}
	if e.count("SELECT count(*) FROM bank_operation_observation WHERE conflict") != 1 {
		t.Fatal("original quarantine evidence lost")
	}
}

func TestNightPaymentDeduplicatesWithMoscowFileDate(t *testing.T) {
	for _, fileFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "file-first", false: "api-first"}[fileFirst], func(t *testing.T) {
			e := newEnv(t)
			setupBank(e)
			inv := e.invoice(500000)
			purpose := "Оплата по счету " + inv.Number
			in := bankIncoming(e, "7", purpose, "night-payment")
			doc := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
			paid := time.Date(2026, 10, 18, 22, 45, 57, 0, time.UTC)
			in.DocDate = &doc
			in.ChargeDate = &paid
			in.OperationDate = paid
			e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
				return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
			})
			file := statementFile(platformAccount, "night-calendar", payment{7, 500000, "7707083893", purpose})
			if fileFirst {
				e.importStatement(file)
			}
			if _, err := e.svc.SyncBank(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !fileFirst {
				if res := e.importStatement(file); res.Duplicates != 1 {
					t.Fatalf("night file result %+v", res)
				}
			}
			if e.balance(e.org) != 500000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
				t.Fatal("night payment credited twice")
			}
			wrongDayKey := clientbank.ExternalID(paid, in.DocumentNumber, in.Amount, in.PayerINN, in.PayerAccount)
			if e.count("SELECT count(*) FROM bank_operation_identity WHERE alias=$1", "external:"+wrongDayKey) != 0 {
				t.Fatal("incorrect previous-day alias persisted")
			}
			correctDayKey := clientbank.ExternalID(paid.In(time.FixedZone("MSK", 3*3600)), in.DocumentNumber, in.Amount, in.PayerINN, in.PayerAccount)
			if e.count("SELECT count(*) FROM bank_operation_identity WHERE alias=$1", "external:"+correctDayKey) != 1 {
				t.Fatal("Moscow date alias missing")
			}
		})
	}
}
func TestBankResumesAfterPageFailure(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	in := bankIncoming(e, "7", "Оплата по счету "+inv.Number, "first")
	fail := true
	seenFrom := time.Time{}
	e.svc.BankSource = pageFunc(func(_ context.Context, q domain.BankQuery) (domain.BankPage, error) {
		if seenFrom.IsZero() {
			seenFrom = q.From
		} else if !q.From.Equal(seenFrom) {
			t.Fatal("period changed on resume")
		}
		if q.Cursor == "" {
			return domain.BankPage{Incoming: []domain.Incoming{in}, NextCursor: "second", Operations: 1}, nil
		}
		if fail {
			return domain.BankPage{}, errors.New("temporary bank failure")
		}
		return domain.BankPage{}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	p, err := e.svc.BankSync.LoadBankProgress(context.Background(), platformAccount)
	if err != nil || p.Cursor != "second" || !p.CompletedTo.IsZero() {
		t.Fatalf("checkpoint %+v %v", p, err)
	}
	fail = false
	if _, err = e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 500000 {
		t.Fatal("balance changed on resume")
	}
	p, _ = e.svc.BankSync.LoadBankProgress(context.Background(), platformAccount)
	if p.LastSuccess.IsZero() || p.CompletedTo.IsZero() || !p.To.IsZero() {
		t.Fatalf("incomplete %+v", p)
	}
}
func TestBankChangedAmountQuarantined(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	in := bankIncoming(e, "7", "Оплата по счету "+inv.Number, "first")
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
	})
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	in.Amount = 700000
	in.ExternalID += "-changed"
	if _, err := e.svc.SyncBank(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.balance(e.org) != 500000 || e.count("SELECT count(*) FROM bank_operation_observation WHERE conflict") != 1 {
		t.Fatal("changed operation credited or conflict lost")
	}
}
