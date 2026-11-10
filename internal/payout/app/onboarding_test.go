package app_test

import (
	"context"
	"errors"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/domain"
)

type onboardingProvider struct {
	domain.Provider
	writes            int
	writeErr, readErr error
}

func (p *onboardingProvider) ExecuteOnboarding(context.Context, string, domain.OnboardingInput) (string, error) {
	p.writes++
	return "invitation_sent", p.writeErr
}
func (p *onboardingProvider) ResolveExecutor(context.Context, domain.ExecutorLookup) (domain.ExecutorResolution, error) {
	if p.readErr != nil {
		return domain.ExecutorResolution{}, p.readErr
	}
	return domain.ExecutorResolution{ExecutorID: 7, Status: domain.ContractorActive}, nil
}

func TestInvitationUncertaintySurvivesRetryWithoutSecondSMS(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "synthetic")
	p := &onboardingProvider{writeErr: errors.New("lost response"), readErr: domain.ErrProviderNotFound}
	e.svc.Provider = p
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	if err := e.svc.RequestOnboarding(ctx, org, "invite", domain.RegistrationNames{}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunOnboarding(ctx, org, "invite"); !isPostponed(err) {
		t.Fatalf("expected uncertainty: %v", err)
	}
	if err := e.svc.RequestOnboarding(ctx, org, "invite", domain.RegistrationNames{}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunOnboarding(ctx, org, "invite"); !isPostponed(err) {
		t.Fatal(err)
	}
	if p.writes != 1 {
		t.Fatalf("resent invitation %d times", p.writes)
	}
	p.readErr = nil
	if err := e.svc.RunOnboarding(ctx, org, "invite"); err != nil {
		t.Fatal(err)
	}
	if p.writes != 1 || e.contractor(org).ExternalID != "7" {
		t.Fatal("recovery duplicated write or lost identity")
	}
	if err := e.svc.RunOnboarding(ctx, org, "invite"); err != nil {
		t.Fatal(err)
	}
	if p.writes != 1 {
		t.Fatal("confirmed checkpoint replayed")
	}
}

func TestEntrepreneurOnboardingRequiresExplicitNames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SoleProprietor, "synthetic")
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	p := &onboardingProvider{}
	e.svc.Provider = p
	if err := e.svc.RequestOnboarding(ctx, org, "register", domain.RegistrationNames{}); err == nil {
		t.Fatal("guessed names")
	}
	if err := e.svc.RequestOnboarding(ctx, org, "register", domain.RegistrationNames{FirstName: "Synthetic", LastName: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.svc.RunOnboarding(ctx, org, "register"); err != nil {
			t.Fatal(err)
		}
	}
	if p.writes != 1 {
		t.Fatal("registration repeated")
	}
}
