package rocketworkfake

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
)

func TestContractorAndPayout(t *testing.T) {
	ctx := context.Background()
	f := New([]byte("s"))
	req := rocketwork.ContractorRequest{ExternalID: "org", INN: "500100732259", LegalForm: rocketwork.LegalFormSelfEmployed}
	c1, err := f.EnsureContractor(ctx, req)
	if err != nil || c1.Status != rocketwork.ContractorInvited || c1.OnboardingURL == "" {
		t.Fatalf("ensure %+v %v", c1, err)
	}
	c2, _ := f.EnsureContractor(ctx, req)
	if c2.ID != c1.ID {
		t.Fatal("EnsureContractor is not idempotent by INN")
	}
	pay := rocketwork.PaymentRequest{IdempotencyKey: "k1", ExternalID: "po-1", ContractorID: c1.ID, AmountKopecks: 10000}
	var rej *rocketwork.RejectedError
	if _, err := f.CreatePayout(ctx, pay); !errors.As(err, &rej) || rej.Code != rocketwork.FailureContractorNotActive {
		t.Fatalf("inactive contractor: %v", err)
	}
	_ = f.ActivateContractor(c1.ID)
	if _, err := f.CreatePayout(ctx, pay); !errors.As(err, &rej) || rej.Code != rocketwork.FailureInsufficientDeposit {
		t.Fatalf("no deposit: %v", err)
	}
	_ = f.SetDeposit(20000)
	p1, err := f.CreatePayout(ctx, pay)
	if err != nil || p1.Status != rocketwork.PaymentSent {
		t.Fatalf("create %+v %v", p1, err)
	}
	p2, _ := f.CreatePayout(ctx, pay)
	if p2.ID != p1.ID || len(f.Payments()) != 1 {
		t.Fatal("CreatePayout is not idempotent by key")
	}
	if err := f.CompletePayout(p1.ID, -1); err != nil {
		t.Fatal(err)
	}
	got, _ := f.GetPayout(ctx, p1.ID)
	if got.Status != rocketwork.PaymentSucceeded || got.FeeKopecks != MinFee+200 || got.ReceiptURL == "" {
		t.Fatalf("completed %+v", got)
	}
	if f.Deposit() != 20000-10000-300 {
		t.Fatalf("deposit %d", f.Deposit())
	}
	if _, found, _ := f.FindPayout(ctx, "k1"); !found {
		t.Fatal("FindPayout")
	}
	_ = f.BlockContractor(c1.ID, rocketwork.FailureTaxStatusLost)
	c, _ := f.GetContractor(ctx, c1.ID)
	if c.Status != rocketwork.ContractorBlocked || c.BlockedReason != rocketwork.FailureTaxStatusLost {
		t.Fatalf("blocked %+v", c)
	}
	f.UnknownStatusNext()
	if c, _ = f.GetContractor(ctx, c1.ID); c.Known || c.Status != rocketwork.ContractorPending {
		t.Fatalf("unknown status %+v", c)
	}
}

func TestInjectedFailures(t *testing.T) {
	ctx := context.Background()
	f := New([]byte("s"))
	_ = f.SetAutoActivate(true)
	_ = f.SetDeposit(1_000_000)
	c, _ := f.EnsureContractor(ctx, rocketwork.ContractorRequest{INN: "1"})
	f.TimeoutNext()
	pay := rocketwork.PaymentRequest{IdempotencyKey: "k", ExternalID: "po", ContractorID: c.ID, AmountKopecks: 100}
	if _, err := f.CreatePayout(ctx, pay); !errors.Is(err, rocketwork.ErrUnavailable) {
		t.Fatalf("timeout: %v", err)
	}
	if _, found, _ := f.FindPayout(ctx, "k"); !found {
		t.Fatal("payment created despite timeout must be found")
	}
	f.FailNext("GetDepositBalance", rocketwork.ErrUnavailable)
	if _, err := f.GetDepositBalance(ctx); !errors.Is(err, rocketwork.ErrUnavailable) {
		t.Fatalf("fail next: %v", err)
	}
	if _, err := f.GetDepositBalance(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if f.CountCalls("GetDepositBalance") != 2 {
		t.Fatalf("calls %v", f.Calls())
	}
}

func TestSignedWebhookAndState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rw.json")
	a := New([]byte("s"), WithStatePath(path))
	b := New([]byte("s"), WithStatePath(path))
	_ = a.SetDeposit(777)
	if b.Deposit() != 777 {
		t.Fatal("state is not shared through the file")
	}
	h, body := a.SignedWebhook(rocketwork.EventContractorStatus, "rwc-1")
	ev, err := b.ParseWebhook(h, body)
	if err != nil || ev.Kind != rocketwork.EventContractorStatus || ev.ObjectID != "rwc-1" {
		t.Fatalf("webhook %+v %v", ev, err)
	}
	if _, err := New([]byte("other")).ParseWebhook(h, body); !errors.Is(err, rocketwork.ErrSignature) {
		t.Fatalf("foreign secret: %v", err)
	}
}
