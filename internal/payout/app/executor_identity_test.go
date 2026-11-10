package app_test

import (
	"context"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/domain"
)

type identityReader struct {
	domain.Provider
	last domain.ExecutorLookup
	id   int64
}

func (p *identityReader) ResolveExecutor(_ context.Context, r domain.ExecutorLookup) (domain.ExecutorResolution, error) {
	p.last = r
	return domain.ExecutorResolution{ExecutorID: p.id, Status: domain.ContractorActive}, nil
}

func TestVerifiedPollBindsIdentityWithoutInvitation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "synthetic")
	if _, err := e.svc.Invite(ctx, org); err != nil {
		t.Fatal(err)
	}
	reader := &identityReader{id: 7} // nil legacy Provider panics if old lookup/invite is used.
	e.svc.Provider = reader
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	c := e.contractor(org)
	if err := e.svc.PollContractor(ctx, c.ID); err != nil && !isPostponed(err) {
		t.Fatal(err)
	}
	if got := e.contractor(org); got.ExternalID != "7" || got.Status != domain.ContractorActive {
		t.Fatalf("not bound: %+v", got)
	}
	if err := e.svc.PollContractor(ctx, c.ID); err != nil && !isPostponed(err) {
		t.Fatal(err)
	}
	if reader.last.ExpectedID != 7 || reader.last.LookupKind != "inn" {
		t.Fatal("binding not used for subsequent poll")
	}
	reader.id = 8
	if err := e.svc.PollContractor(ctx, c.ID); err == nil || isPostponed(err) {
		t.Fatal("conflicting provider identity rebound")
	}
	if got := e.contractor(org); got.ExternalID != "7" {
		t.Fatal("conflict changed binding")
	}
}
