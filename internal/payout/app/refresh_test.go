package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
)

func TestRefreshRequestsCheckWithoutProviderCall(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	org := e.blogger(accountdomain.SelfEmployed)
	c := e.contractor(org)
	calls := e.rw.CountCalls("GetContractor")
	e.clk.Advance(time.Minute)
	for range 2 {
		v, err := e.svc.Refresh(ctx, org)
		if err != nil || v.Status != c.Status || v.StatusCheckedAt == nil || !v.StatusCheckedAt.Equal(*c.StatusCheckedAt) {
			t.Fatalf("refresh changed checked status: %+v %v", v, err)
		}
	}
	if e.rw.CountCalls("GetContractor") != calls || e.jobs(app.PollContractorKey(c.ID)) != 1 {
		t.Fatal("synchronous provider call or duplicate job")
	}
	if err := e.svc.PollContractor(ctx, c.ID); err != nil && !isPostponed(err) {
		t.Fatal(err)
	}
	checked := e.contractor(org)
	if checked.Status != c.Status || checked.StatusCheckedAt == nil || !checked.StatusCheckedAt.Equal(e.clk.Now()) {
		t.Fatal("unchanged provider status did not update checked_at")
	}
	// Provider changes status; Refresh still returns the last known active value.
	if err := e.rw.BlockContractor(c.ExternalID, rocketwork.FailureTaxStatusLost); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	v, err := e.svc.Refresh(ctx, org)
	if err != nil || v.Status != domain.ContractorActive {
		t.Fatalf("premature status: %+v %v", v, err)
	}
	if err := e.svc.PollContractor(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	v, err = e.svc.Status(ctx, org)
	if err != nil || v.Status != domain.ContractorBlocked || v.NextAction == "none" || !v.StatusCheckedAt.Equal(e.clk.Now()) {
		t.Fatalf("worker outcome: %+v %v", v, err)
	}
}

func TestRefreshNoneDisabledAndRateLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bare := ledgertest.Organization(t, e.d.Pool, accountdomain.SelfEmployed, "No contractor")
	calls := e.rw.CountCalls("GetContractor")
	v, err := e.svc.Refresh(ctx, bare)
	if err != nil || v.Status != domain.ContractorNone || v.StatusCheckedAt != nil || e.rw.CountCalls("GetContractor") != calls || e.count(`SELECT count(*) FROM job`) != 0 {
		t.Fatalf("none refresh: %+v %v", v, err)
	}
	org := e.blogger(accountdomain.SelfEmployed)
	e.svc.Config.ProviderEnabled = false
	if _, err := e.svc.Refresh(ctx, org); !errors.Is(err, domain.ErrProviderDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	e.svc.Config.ProviderEnabled = true
	e.svc.Limits = e.svc.Audit.(*audit.Log)
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorBlogger, UserID: &e.admin, OrganizationID: &org})
	for range app.CabinetRequestsPerHour {
		if _, err := e.svc.RefreshFromCabinet(ctx, org, e.admin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.RefreshFromCabinet(ctx, org, e.admin); !apperr.Is(err, apperr.CodeRateLimited) {
		t.Fatalf("refresh rate limit: %v", err)
	}
}
