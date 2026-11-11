package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestCheckInvoicePaymentFreshWindowAndSharedCooldown(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	ctx := context.Background()
	old := domain.BankProgress{Account: platformAccount, From: start.Add(-48 * time.Hour), To: start.Add(-24 * time.Hour), Cursor: "pending-old", StatementID: uuid.NewString(), CompletedTo: start.Add(-72 * time.Hour)}
	if err := e.svc.BankSync.SaveBankProgress(ctx, old); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.svc.BankSource = pageFunc(func(_ context.Context, q domain.BankQuery) (domain.BankPage, error) {
		calls++
		if !q.To.Equal(e.clk.Now()) || q.From.Before(e.clk.Now().Add(-24*time.Hour)) || q.Cursor != "" {
			t.Fatalf("not a fresh query: %+v", q)
		}
		return domain.BankPage{}, nil
	})
	first, err := e.svc.CheckInvoicePayment(ctx, e.org, inv.ID)
	if err != nil || first.Result != "not_found" || first.CheckedAt == nil || calls != 1 {
		t.Fatalf("check %+v %v", first, err)
	}
	p, err := e.svc.BankSync.LoadBankProgress(ctx, platformAccount)
	if err != nil || !p.From.Equal(old.From) || p.Cursor != old.Cursor || p.StatementID != old.StatementID || !p.CompletedTo.Equal(old.CompletedTo) {
		t.Fatalf("background checkpoint changed: %+v %v", p, err)
	}
	// A new API service instance still observes the durable account cooldown.
	other := app.New(e.svc.Deps)
	second, err := other.CheckInvoicePayment(ctx, e.org, inv.ID)
	if err != nil || second.Result != "busy" || second.RetryAfter != 60 || calls != 1 {
		t.Fatalf("cooldown %+v %v", second, err)
	}
	e.clk.Advance(61 * time.Second)
	if _, err := other.CheckInvoicePayment(ctx, e.org, inv.ID); err != nil || calls != 2 {
		t.Fatalf("retry %v calls=%d", err, calls)
	}
}

func TestCheckInvoicePaymentCreditsOnceAndPreservesDevGate(t *testing.T) {
	for _, marker := range []bool{true, false} {
		t.Run(map[bool]string{true: "paid", false: "missing dev marker"}[marker], func(t *testing.T) {
			e := newEnv(t)
			controlledBank(e)
			inv := e.invoice(500000)
			e.clk.Advance(time.Minute)
			purpose := "Оплата по счёту " + inv.Number
			if marker {
				purpose += " " + e.svc.BankConfig.PaymentMarker()
			}
			in := bankIncoming(e, "check", purpose, "manual-check")
			e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
				return domain.BankPage{Incoming: []domain.Incoming{in}}, nil
			})
			result, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !marker {
				if result.Result != "not_found" || e.balance(e.org) != 0 {
					t.Fatalf("dev gate bypass: %+v", result)
				}
				return
			}
			if result.Result != "paid" || result.Invoice.Status != domain.InvoicePaid || e.balance(e.org) != 500000 {
				t.Fatalf("payment %+v", result)
			}
			again, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
			if err != nil || again.Result != "paid" || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
				t.Fatal("duplicate deposit")
			}
		})
	}
}

func TestCheckInvoicePaymentOwnershipAndUnavailable(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	var calls atomic.Int32
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		calls.Add(1)
		return domain.BankPage{}, errors.New("bank unavailable")
	})
	if _, err := e.svc.CheckInvoicePayment(context.Background(), uuid.New(), inv.ID); !isCode(err, apperr.CodeNotFound) || calls.Load() != 0 {
		t.Fatalf("ownership: %v", err)
	}
	e.svc.BankConfig.Access = domain.BankReadOnly
	result, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || result.Result != "unavailable" || calls.Load() != 0 {
		t.Fatal("readonly made a bank call")
	}
	e.svc.BankConfig.Access = domain.BankAutomation
	result, err = e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || result.Result != "unavailable" || result.CheckedAt != nil || e.balance(e.org) != 0 {
		t.Fatalf("outage called not found: %+v %v", result, err)
	}
	result, err = e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || result.Result != "busy" || calls.Load() != 1 {
		t.Fatal("failed request did not consume cooldown")
	}
}

func TestCheckInvoicePaymentConcurrentLock(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	started, release := make(chan struct{}), make(chan struct{})
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		close(started)
		<-release
		return domain.BankPage{}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID); done <- err }()
	<-started
	result, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	close(release)
	if err != nil || result.Result != "busy" {
		t.Errorf("concurrent request: %+v %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCheckInvoicePaymentCursorCycleIsUnavailable(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{NextCursor: "same"}, nil
	})
	result, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || result.Result != "unavailable" || result.CheckedAt != nil {
		t.Fatalf("partial statement claimed success: %+v %v", result, err)
	}
}

func TestCheckInvoicePaymentPartialReadPreservesConfirmedDeposit(t *testing.T) {
	e := newEnv(t)
	setupBank(e)
	inv := e.invoice(500000)
	e.clk.Advance(time.Minute)
	in := bankIncoming(e, "partial", "Оплата по счёту "+inv.Number, "partial-check")
	calls := 0
	e.svc.BankSource = pageFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		calls++
		if calls == 1 {
			return domain.BankPage{Incoming: []domain.Incoming{in}, NextCursor: "next"}, nil
		}
		return domain.BankPage{}, errors.New("connection lost")
	})
	result, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || result.Result != "paid" || result.CheckedAt != nil || e.balance(e.org) != 500000 {
		t.Fatalf("confirmed payment lost: %+v %v", result, err)
	}
	again, err := e.svc.CheckInvoicePayment(context.Background(), e.org, inv.ID)
	if err != nil || again.Result != "paid" || calls != 2 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
		t.Fatal("partial read caused duplicate")
	}
}
