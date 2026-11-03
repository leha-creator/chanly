package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Отчёт по заказу после публикации и снятия (R-14 этапа уведомлений): просмотры, ссылка на пост,
// удержание до приёмки; уведомление о снятии несёт просмотры и срок претензии.
func TestOrderReportAfterRemoval(t *testing.T) {
	e := newEnv(t)
	pid, _, msgID := e.published()
	e.max.SetViews(msgID, 1200)
	e.clk.Set(e.placement(pid).RemoveAt.Add(4 * time.Second))
	e.runJobs()
	p := e.placement(pid)

	r, err := e.svc.OrderReport(context.Background(), e.adv, p.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 || r.UTM.Campaign != domain.UTMCampaign(r.Order.Number) || r.Links == nil {
		t.Fatalf("report %+v", r)
	}
	row := r.Rows[0]
	if row.Status != domain.StatusRemoved || row.Views == nil || *row.Views != 1200 || row.PublicationURL == "" ||
		row.Held != p.TotalAmount || row.Spent != 0 || row.CPV != nil || row.PublishedAt == nil || row.RemovedAt == nil {
		t.Fatalf("row %+v", row)
	}
	if r.Totals.Views != 1200 || r.Totals.Published != 1 || r.Totals.Held != p.TotalAmount {
		t.Fatalf("totals %+v", r.Totals)
	}
	if len(e.logged("order report built")) != 1 {
		t.Fatal("report not logged")
	}

	n, ok := e.notices.Last(domain.NoticeRemoved, domain.ActorAdvertiser)
	if !ok || n.Data["views"] != 1200 || n.Data["claim_deadline_at"] == nil || n.Data["order_id"] != p.OrderID {
		t.Fatalf("removed notice %+v", n)
	}
	if pub, ok := e.notices.Last(domain.NoticePublished, domain.ActorAdvertiser); !ok || pub.Data["publication_url"] == "" {
		t.Fatalf("published notice %+v", pub)
	}

	other, _ := e.otherAdvertiser()
	if _, err := e.svc.OrderReport(context.Background(), other, p.OrderID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign order: %v", err)
	}
}
