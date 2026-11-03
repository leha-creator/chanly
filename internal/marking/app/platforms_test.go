package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/markingtest"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

var chatSeq int64 = -7_000_000_000

// moderatedChannel — канал блогера на модерации, готовый к одобрению.
func (e *env) moderatedChannel(org uuid.UUID, title string) uuid.UUID {
	e.t.Helper()
	chatSeq--
	id := ids.New()
	ctx := context.Background()
	if _, err := e.d.Pool.Exec(ctx, `INSERT INTO channel (id, organization_id, max_chat_id, title, link, added_by_max_user_id, status,
		is_public, category_id, bot_is_admin, bot_permissions, subscribers_count, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'https://max.ru/join/cook', 7, 'moderation', true, $5, true, ARRAY['write', 'delete'], 5000, now(), now())`,
		id, org, chatSeq, title, placementtest.CategoryFood); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.d.Pool.Exec(ctx, `INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
		VALUES ($1, $2, 'f_1_24', 1000000, true, now(), now())`, ids.New(), id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// approveChannel — администратор одобряет канал (S-16 шаг 3).
func (e *env) approveChannel(id uuid.UUID) {
	e.t.Helper()
	ctx := audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdmin, UserID: &e.advUser})
	if _, err := e.channels.Approve(ctx, e.advUser, id); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) inCatalog(id uuid.UUID) bool {
	e.t.Helper()
	page, err := e.channels.ListCatalog(context.Background(), channelapp.CatalogRequest{Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func (e *env) platformOf(channel uuid.UUID) domain.Platform {
	e.t.Helper()
	p, err := e.store.Platforms().ByChannel(context.Background(), channel)
	if err != nil {
		e.t.Fatalf("platform of %s: %v", channel, err)
	}
	return p
}

func TestChannelApprovalRegistersPlatform(t *testing.T) {
	e := newEnv(t)
	blogger := markingtest.Organization(t, e.d.Pool, "self_employed", "Блогер")
	markingtest.Accept(t, e.d.Pool, blogger, markingtest.BloggerOffer, start.Add(-24*time.Hour))
	ch := e.moderatedChannel(blogger, "  Кулинарный канал  ")
	e.approveChannel(ch)

	if p := e.platformOf(ch); p.Sync.Status != domain.SyncPending || p.Name != "Кулинарный канал" || p.OrganizationID != blogger {
		t.Fatalf("platform after approval: %+v", p)
	}
	if e.inCatalog(ch) {
		t.Fatal("channel in catalog before ord platform is ready")
	}
	e.settle(2)
	var body ord.Platforms
	obj, ok := e.ord.Object(ch.String())
	if !ok || obj.Decode(&body) != nil || body.OrganizationID != blogger.String() || len(body.Platforms) != 1 ||
		body.Platforms[0].Type != "site" || body.Platforms[0].IsOwned || body.Platforms[0].URL != "https://max.ru/join/cook" ||
		body.Platforms[0].Name != "Кулинарный канал" {
		t.Fatalf("platform request: %+v", body)
	}
	if o := e.org(blogger); !o.IsRr {
		t.Fatalf("blogger organization: %+v", o)
	}
	e.settle(4)
	if !e.inCatalog(ch) {
		t.Fatalf("channel not in catalog after ord platform: %+v", e.platformOf(ch).Sync)
	}
	if c := e.contract(domain.ContractPublisher, e.platform, blogger); c.Sync.Status == domain.SyncPending {
		t.Fatalf("publisher contract not sent: %+v", c.Sync)
	}
	if len(e.logged("channel visible after ord platform")) == 0 {
		t.Fatal("visibility not logged")
	}

	// Повторное одобрение с тем же названием — без вызова ОРД.
	calls := e.ord.Calls(ord.OpPlatforms)
	if _, err := e.d.Pool.Exec(context.Background(), `UPDATE channel SET status = 'moderation' WHERE id = $1`, ch); err != nil {
		t.Fatal(err)
	}
	e.approveChannel(ch)
	e.settle(2)
	if e.ord.Calls(ord.OpPlatforms) != calls || !e.inCatalog(ch) {
		t.Fatal("repeated approval with the same title called ORD")
	}
}

func TestPlatformErrorKeepsChannelOutOfCatalog(t *testing.T) {
	e := newEnv(t)
	blogger := markingtest.Organization(t, e.d.Pool, "self_employed", "Блогер")
	ch := e.moderatedChannel(blogger, "Канал")
	e.ord.FailNext(ord.OpPlatforms, http.StatusUnprocessableEntity, "body.platforms.0.url: invalid")
	e.approveChannel(ch)
	e.settle(4)
	if p := e.platformOf(ch); p.Sync.Status != domain.SyncError {
		t.Fatalf("platform: %+v", p.Sync)
	}
	var status string
	_ = e.d.Pool.QueryRow(context.Background(), `SELECT status FROM channel WHERE id = $1`, ch).Scan(&status)
	if status != "active" || e.inCatalog(ch) {
		t.Fatalf("channel %s, in catalog %v", status, e.inCatalog(ch))
	}
	if !e.alerts.has("ord_sync_failed:platform") {
		t.Fatal("no platform alert")
	}
}

func TestAdvertiserBecomesBlogger(t *testing.T) {
	e := newEnv(t)
	e.initialChain(e.adv)
	e.settle(5)
	if o := e.org(e.adv); o.IsRr || o.Sync.Status != domain.SyncSynced {
		t.Fatalf("advertiser: %+v", o)
	}
	markingtest.Accept(t, e.d.Pool, e.adv, markingtest.BloggerOffer, start)
	ch := e.moderatedChannel(e.adv, "Канал рекламодателя")
	e.approveChannel(ch)
	e.settle(3)
	var body ord.Organization
	obj, _ := e.ord.Object(e.adv.String())
	if obj.Submits != 2 || obj.Decode(&body) != nil || !body.IsRr {
		t.Fatalf("organization resubmit: %d %+v", obj.Submits, body)
	}
	if e.count(`SELECT count(*) FROM ord_organization WHERE organization_id = $1`, e.adv) != 1 {
		t.Fatal("second contractor for the same organization")
	}
}
