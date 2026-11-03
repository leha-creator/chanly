package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/domain"
)

type approvals struct {
	got []domain.ApprovedChannel
	err error
}

func (a *approvals) ChannelApproved(_ context.Context, ch domain.ApprovedChannel) error {
	a.got = append(a.got, ch)
	return a.err
}

func TestApprovalPassedToMarking(t *testing.T) {
	e := newEnv(t)
	l := &approvals{}
	e.svc.SetMarkingPorts(l)
	adminID, ctx := e.admin()

	c := e.moderated(-80000000041)
	if _, err := e.svc.Approve(ctx, adminID, c.ID); err != nil {
		t.Fatal(err)
	}
	if len(l.got) != 1 || l.got[0].ID != c.ID || l.got[0].OrganizationID != *c.OrganizationID || l.got[0].Title != c.Title {
		t.Fatalf("listener calls %+v", l.got)
	}
	// R-14: канал active, но до регистрации площадки его нет в каталоге.
	if page, _ := e.svc.ListCatalog(context.Background(), app.CatalogRequest{}); len(page.Items) != 0 {
		t.Fatal("channel listed before ord platform is ready")
	}
	if err := e.svc.SetOrdPlatformReady(context.Background(), c.ID, e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if page, _ := e.svc.ListCatalog(context.Background(), app.CatalogRequest{}); len(page.Items) != 1 {
		t.Fatal("channel not listed after ord platform is ready")
	}

	// Ошибка слушателя откатывает одобрение.
	l.err = errors.New("boom")
	other := e.moderated(-80000000042)
	if _, err := e.svc.Approve(ctx, adminID, other.ID); err == nil {
		t.Fatal("approve succeeded with failing listener")
	}
	if got := e.channel(-80000000042); got.Status != domain.StatusModeration {
		t.Fatalf("status after rollback: %s", got.Status)
	}
}

func TestApprovalWithoutMarkingMakesChannelVisible(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000043)
	if c.OrdPlatformReadyAt == nil {
		t.Fatal("ORD disabled: approval must set ord_platform_ready_at")
	}
}
