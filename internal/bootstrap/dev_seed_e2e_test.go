package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/channel/app"
)

// testWaiter — ожидание dev seed в тесте: прогоняет worker тестового приложения и двигает
// часы на минуту между проверками (R-4 плана dev seed).
type testWaiter struct{ a *testApp }

func (w testWaiter) Wait(ctx context.Context, what string, done func(ctx context.Context) (bool, error)) error {
	for range 180 {
		w.a.settle()
		ok, err := done(ctx)
		if err != nil || ok {
			return err
		}
		w.a.clk.Advance(time.Minute)
	}
	return fmt.Errorf("worker did not %s", what)
}

func (a *testApp) seeder(out *bytes.Buffer, orders bool) *seeder {
	return &seeder{cfg: a.mods.cfg, m: a.mods, log: a.mods.in.log, out: out, orders: orders,
		wait: testWaiter{a: a}, rw: a.rocketWork()}
}

// План dev seed, задача 8: команда на собранных api и worker создаёт данные в целевых
// состояниях, повтор ничего не дублирует, журнал проводок сходится.
func TestDevSeedEndToEnd(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	var out bytes.Buffer
	if err := a.seeder(&out, true).run(ctx); err != nil {
		t.Fatalf("seed: %v\n%s", err, out.String())
	}
	a.ledgerOK("after seed")

	for _, email := range []string{seedAdvEmail, seedBlgEmail, seedBlgULEmail, seedAdminEmail, seedModeratorEmail} {
		if a.count(`SELECT count(*) FROM "user" WHERE email = $1`, email) != 1 {
			t.Fatalf("user %s", email)
		}
	}
	page, err := a.mods.channel.ListCatalog(ctx, app.CatalogRequest{})
	if err != nil || len(page.Items) != 8 {
		t.Fatalf("catalog: %d %v", len(page.Items), err)
	}
	if n := a.count(`SELECT count(*) FROM channel WHERE status = 'moderation'`); n != 1 {
		t.Fatalf("moderation queue: %d", n)
	}
	if n := a.count(`SELECT count(*) FROM channel WHERE status = 'active' AND ord_platform_ready_at IS NOT NULL`); n != 8 {
		t.Fatalf("channels registered in ORD: %d", n)
	}
	// erid выдал worker через ordfake, а не обход маркировки.
	if n := a.count(`SELECT count(*) FROM creative cr JOIN "order" o ON o.current_post_version_id = cr.post_version_id
		JOIN campaign c ON c.id = o.campaign_id WHERE c.name = 'Демо: awaiting_blogger' AND cr.erid IS NOT NULL`); n != 1 {
		t.Fatalf("erid of awaiting_blogger: %d", n)
	}
	want := map[string]string{
		"moderation": "created", "rejected": "created", "cancelled": "cancelled", "awaiting_blogger": "awaiting_blogger",
		"awaiting_schedule": "awaiting_schedule", "scheduled": "scheduled", "published": "published", "disputed": "disputed",
		"removed": "removed", "accepted": "accepted", "paid": "paid", "bank_transfer": "accepted",
	}
	for scenario, status := range want {
		var got string
		if err := a.db.Pool.QueryRow(ctx, `SELECT p.status FROM placement p JOIN "order" o ON o.id = p.order_id
			JOIN campaign c ON c.id = o.campaign_id WHERE c.name = $1`, "Демо: "+scenario).Scan(&got); err != nil || got != status {
			t.Errorf("scenario %s: placement %q, want %q (%v)", scenario, got, status, err)
		}
	}
	var method string
	if err := a.db.Pool.QueryRow(ctx, `SELECT py.method FROM payout py JOIN placement p ON p.id = py.placement_id
		JOIN "order" o ON o.id = p.order_id JOIN campaign c ON c.id = o.campaign_id WHERE c.name = 'Демо: bank_transfer'`).
		Scan(&method); err != nil || method != "bank_transfer" {
		t.Errorf("bank transfer payout: %q %v", method, err)
	}
	if n := a.count(`SELECT count(*) FROM campaign WHERE name IN ('Демо: черновик', 'Демо: пустая')`); n != 2 {
		t.Fatalf("campaigns without orders: %d", n)
	}
	if !strings.Contains(out.String(), seedAdvEmail) || strings.Contains(out.String(), "Пропущено") {
		t.Fatalf("summary:\n%s", out.String())
	}

	// Вход по коду — как у любого пользователя.
	adv := a.client()
	adv.login(seedAdvEmail)
	if r := adv.do(http.MethodGet, "/orders", nil); r.status != http.StatusOK || !strings.Contains(string(r.body), "Демо: paid") {
		t.Fatalf("orders of seeded advertiser: %d %s", r.status, r.body)
	}

	// Повтор ничего не создаёт.
	counts := func() string {
		return fmt.Sprint(a.count(`SELECT count(*) FROM "user"`), a.count(`SELECT count(*) FROM organization`),
			a.count(`SELECT count(*) FROM channel`), a.count(`SELECT count(*) FROM campaign`),
			a.count(`SELECT count(*) FROM "order"`), a.count(`SELECT count(*) FROM ledger_transaction`),
			a.count(`SELECT count(*) FROM legal_document`))
	}
	before := counts()
	out.Reset()
	if err := a.seeder(&out, true).run(ctx); err != nil {
		t.Fatalf("second seed: %v\n%s", err, out.String())
	}
	if after := counts(); after != before {
		t.Fatalf("second seed changed data: %s → %s\n%s", before, after, out.String())
	}
	if !strings.Contains(out.String(), "Создано: 0") || !strings.Contains(out.String(), "paid: exists") {
		t.Fatalf("second summary:\n%s", out.String())
	}
	a.ledgerOK("after second seed")
}

// Без общей подделки Рокет Ворк сценарии с исполнителем пропускаются, остальное создаётся;
// при ORD_ENABLED=false версия готова без маркировки.
func TestDevSeedWithoutRocketWork(t *testing.T) {
	cfg := testConfig()
	cfg.ORD.Enabled = false
	a := newTestAppWith(t, cfg)
	var out bytes.Buffer
	s := a.seeder(&out, true)
	s.rw = nil
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("seed: %v\n%s", err, out.String())
	}
	for _, line := range []string{"paid: rocketwork_not_fake", "scheduled: rocketwork_not_fake", "заказ"} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("summary has no %q:\n%s", line, out.String())
		}
	}
	if n := a.count(`SELECT count(*) FROM placement WHERE status = 'accepted'`); n != 1 {
		t.Fatalf("bank transfer placement without rocketwork: %d", n)
	}
	a.ledgerOK("seed without rocketwork")
}

func TestDevSeedCommand(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	prod := testConfig()
	prod.Env = EnvProduction
	if err := runDev(ctx, prod, a.mods.in.log, []string{"seed"}, &bytes.Buffer{}); !errors.Is(err, errDevForbidden) {
		t.Fatalf("production: %v", err)
	}
	for _, args := range [][]string{{"seed", "extra"}, {"seed", "--wait", "0s"}, {"seed", "--unknown"}} {
		if err := runDev(ctx, testConfig(), a.mods.in.log, args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestSeedHelpers(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	for _, tc := range []struct {
		now, want time.Time
	}{
		{time.Date(2026, 10, 16, 12, 0, 0, 0, msk), time.Date(2026, 10, 16, 13, 5, 0, 0, msk)},
		{time.Date(2026, 10, 16, 12, 1, 30, 0, msk), time.Date(2026, 10, 16, 13, 10, 0, 0, msk)},
		{time.Date(2026, 10, 16, 22, 52, 0, 0, msk), time.Date(2026, 10, 17, 0, 0, 0, 0, msk)},
		{time.Date(2026, 10, 16, 22, 47, 0, 0, msk), time.Date(2026, 10, 17, 0, 5, 0, 0, msk)},
		{time.Date(2026, 10, 16, 22, 45, 0, 0, msk), time.Date(2026, 10, 16, 23, 50, 0, 0, msk)},
	} {
		if got := seedPublishAt(tc.now, time.Hour, 5*time.Minute); !got.Equal(tc.want) {
			t.Errorf("publish at for %s: %s, want %s", tc.now, got.In(msk), tc.want)
		}
	}
	// 21:30 UTC 16-го — 00:30 17-го по Москве: окно от московской даты.
	from, to := seedCampaignWindow(time.Date(2026, 10, 16, 21, 30, 0, 0, time.UTC))
	if from.Format(time.DateOnly) != "2026-10-17" || to.Format(time.DateOnly) != "2026-10-19" {
		t.Fatalf("window: %s … %s", from, to)
	}
	for _, lt := range []accountdomain.LegalType{accountdomain.LegalEntity, accountdomain.SelfEmployed} {
		prefix := "770900001"
		if lt != accountdomain.LegalEntity {
			prefix = "5009000001"
		}
		if inn := seedINN(prefix, lt); accountdomain.CheckINN(inn, lt) != "" || !strings.HasPrefix(inn, prefix) {
			t.Fatalf("inn %s for %s", inn, lt)
		}
	}
	if pdf := seedPDF("demo (1)"); http.DetectContentType(pdf) != "application/pdf" || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("pdf: %q", pdf[:20])
	}
	if png := seedPNG(color.RGBA{A: 255}); http.DetectContentType(png) != "image/png" {
		t.Fatal("png")
	}
}
