package app_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// fakePlacements — сведения о размещениях для отчёта агента.
type fakePlacements map[uuid.UUID]domain.PlacementInfo

func (f fakePlacements) Info(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.PlacementInfo, error) {
	out := map[uuid.UUID]domain.PlacementInfo{}
	for _, id := range ids {
		if i, ok := f[id]; ok {
			out[id] = i
		}
	}
	return out, nil
}

// settled — размещение рекламодателя, удержание и распределение в момент at: блогеру его
// цена, платформе комиссия.
func (e *env) settled(at time.Time, info fakePlacements) ledgertest.Placement {
	e.t.Helper()
	p := ledgertest.NewPlacement(e.t, e.d.Pool, e.org, 1_000_000)
	e.clk.Set(at)
	err := e.ledger.Tx.InTx(context.Background(), func(ctx context.Context) error {
		if err := e.ledger.HoldAll(ctx, e.org, []ledgerapp.HoldRequest{{PlacementID: p.ID, PostVersionID: p.VersionID, Amount: p.TotalAmount}}); err != nil {
			return err
		}
		return e.ledger.Settle(ctx, p.ID, p.BloggerID, ledgerdomain.Distribution{Payout: p.BloggerAmount, FeeCharged: p.FeeAmount},
			ledgerdomain.ActorSystem, uuid.Nil)
	})
	if err != nil {
		e.t.Fatalf("settle: %v", err)
	}
	published := at.Add(-48 * time.Hour)
	info[p.ID] = domain.PlacementInfo{OrderNumber: int64(len(info) + 1), ChannelTitle: "Канал", PublishedAt: &published, RemovedAt: &at}
	return p
}

func TestGenerateClosingActs(t *testing.T) {
	e := newEnv(t)
	info := fakePlacements{}
	e.svc.Placements = info
	inv := e.invoice(10_000_000)
	e.importStatement(statementFile(platformAccount, "Сбербанк", payment{1, 10_000_000, "7707083893", inv.Number}))

	// 23:59:59 31 октября по Москве — ещё октябрь; 00:00 1 ноября — уже ноябрь.
	p1 := e.settled(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), info)
	p2 := e.settled(time.Date(2026, 10, 31, 20, 59, 59, 0, time.UTC), info)
	p3 := e.settled(time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC), info)
	e.clk.Set(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) // 03:00 МСК 1 ноября

	october := domain.PreviousPeriod(e.clk.Now())
	if october.String() != "2026-10" {
		t.Fatalf("period %s", october)
	}
	ctx := context.Background()
	res, err := e.svc.GenerateClosingActs(ctx, october)
	if err != nil || res.Created != 1 || res.Skipped != 0 || res.Failed != 0 {
		t.Fatalf("october: %+v, %v", res, err)
	}
	page, err := e.svc.ListClosingActs(ctx, e.org, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %+v, %v", page, err)
	}
	act := page.Items[0]
	if act.Number != "А-000001" || act.Period != october || act.PlacementsAmount != p1.BloggerAmount+p2.BloggerAmount ||
		act.FeeAmount != p1.FeeAmount+p2.FeeAmount {
		t.Fatalf("act %+v", act)
	}
	for _, doc := range []domain.ClosingDocument{domain.DocumentReport, domain.DocumentAct} {
		_, body, _, err := e.svc.ClosingActPDF(ctx, e.org, act.ID, doc)
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		data, _ := io.ReadAll(body)
		body.Close()
		if !bytes.HasPrefix(data, []byte("%PDF")) {
			t.Fatalf("%s is not a PDF", doc)
		}
	}
	if _, _, _, err := e.svc.ClosingActPDF(ctx, e.admin, act.ID, domain.DocumentAct); !isCode(err, apperr.CodeNotFound) {
		t.Fatalf("foreign act: %v", err)
	}
	if _, _, _, err := e.svc.ClosingActPDF(ctx, e.org, act.ID, "bogus"); !isCode(err, apperr.CodeValidationFailed) {
		t.Fatalf("bad document: %v", err)
	}

	// Повторный запуск — без новых актов.
	if res, err := e.svc.GenerateClosingActs(ctx, october); err != nil || res.Created != 0 || res.Skipped != 1 {
		t.Fatalf("repeat: %+v, %v", res, err)
	}
	november, _ := domain.ParsePeriod("2026-11")
	if res, err := e.svc.GenerateClosingActs(ctx, november); err != nil || res.Created != 1 {
		t.Fatalf("november: %+v, %v", res, err)
	}
	page, _ = e.svc.ListClosingActs(ctx, e.org, "", 1)
	if len(page.Items) != 1 || page.Items[0].Period != november || page.Items[0].PlacementsAmount != p3.BloggerAmount ||
		page.NextCursor == "" {
		t.Fatalf("newest first: %+v", page)
	}
	page, _ = e.svc.ListClosingActs(ctx, e.org, page.NextCursor, 1)
	if len(page.Items) != 1 || page.Items[0].ID != act.ID || page.NextCursor != "" {
		t.Fatalf("second page: %+v", page)
	}
	// Организация без распределений и пустой период — без акта.
	september, _ := domain.ParsePeriod("2026-09")
	if res, err := e.svc.GenerateClosingActs(ctx, september); err != nil || res != (app.GenerateResult{}) {
		t.Fatalf("september: %+v, %v", res, err)
	}
	if n := e.count(`SELECT count(*) FROM closing_act`); n != 2 {
		t.Fatalf("closing acts %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'closing_act.generated'`); n != 2 {
		t.Fatalf("audit %d", n)
	}
}
