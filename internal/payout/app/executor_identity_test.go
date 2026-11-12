package app_test

import (
	"context"
	"errors"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
)

type identityReader struct {
	domain.Provider
	last  domain.ExecutorLookup
	id    int64
	err   error
	reads int
}

func (p *identityReader) ResolveExecutor(_ context.Context, r domain.ExecutorLookup) (domain.ExecutorResolution, error) {
	p.last = r
	p.reads++
	return domain.ExecutorResolution{ExecutorID: p.id, Status: domain.ContractorActive}, p.err
}

func TestConnectExistingExecutorAndRefreshWithoutInvitation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "synthetic")
	reader := &identityReader{id: 7} // No onboarding writer: any invitation would fail the test.
	e.svc.Provider = reader
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	if _, err := e.svc.Invite(ctx, org); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 0 {
		t.Fatal("HTTP-facing connect called provider synchronously")
	}
	if _, err := e.svc.Refresh(ctx, org); err != nil {
		t.Fatal(err)
	}
	c := e.contractor(org)
	if e.jobs(app.PollContractorKey(c.ID)) != 1 {
		t.Fatal("refresh did not queue a read for an unbound executor")
	}
	if err := e.svc.PollContractor(ctx, c.ID); err != nil && !isPostponed(err) {
		t.Fatal(err)
	}
	if err := e.svc.EnsureContractor(ctx, org); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Status(ctx, org)
	if err != nil || v.Status != domain.ContractorActive || v.NextAction != "none" || e.contractor(org).ExternalID != "7" {
		t.Fatalf("existing executor not connected: %+v, %v", v, err)
	}
	if n := e.count("SELECT count(*) FROM rocketwork_onboarding"); n != 0 {
		t.Fatal("existing executor caused an onboarding write")
	}
}

func TestExecutorLookupFailureIsVisibleAndRecovers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "synthetic")
	reader := &identityReader{id: 7, err: errors.New("identity mismatch")}
	e.svc.Provider = reader
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	if _, err := e.svc.Invite(ctx, org); err != nil {
		t.Fatal(err)
	}
	lookupErr := reader.err
	reader.err = domain.ErrProviderNotFound
	if err := e.svc.PollContractor(ctx, e.contractor(org).ID); !errors.Is(err, domain.ErrProviderNotFound) {
		t.Fatal(err)
	}
	if got := e.contractor(org); got.LastError != "" {
		t.Fatal("expected absence of an unbound executor shown as lookup failure")
	}
	reader.err = lookupErr
	if err := e.svc.EnsureContractor(ctx, org); !errors.Is(err, reader.err) {
		t.Fatalf("lookup error lost: %v", err)
	}
	v, err := e.svc.Status(ctx, org)
	if err != nil || v.NextAction != "contact_support" || e.contractor(org).ExternalID != "" {
		t.Fatalf("lookup failure hidden: %+v, %v", v, err)
	}
	reader.err = nil
	c := e.contractor(org)
	if err := e.svc.PollContractor(ctx, c.ID); err != nil && !isPostponed(err) {
		t.Fatal(err)
	}
	if got := e.contractor(org); got.LastError != "" || got.Status != domain.ContractorActive {
		t.Fatalf("successful lookup did not clear failure: %+v", got)
	}
	reader.err = errors.New("provider unavailable")
	if err := e.svc.PollContractor(ctx, c.ID); !errors.Is(err, reader.err) {
		t.Fatal(err)
	}
	if v, err := e.svc.Status(ctx, org); err != nil || v.NextAction != "contact_support" {
		t.Fatalf("poll lookup failure hidden: %+v, %v", v, err)
	}
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
