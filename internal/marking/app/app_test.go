package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	accountapp "github.com/leha-creator/chanly/internal/account/app"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/integration/ord/ordfake"
	"github.com/leha-creator/chanly/internal/marking/app"
	"github.com/leha-creator/chanly/internal/marking/domain"
	markingjobs "github.com/leha-creator/chanly/internal/marking/jobs"
	"github.com/leha-creator/chanly/internal/marking/markingtest"
	markingpg "github.com/leha-creator/chanly/internal/marking/postgres"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementjobs "github.com/leha-creator/chanly/internal/placement/jobs"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging"
	postjobs "github.com/leha-creator/chanly/internal/post/jobs"
	postpg "github.com/leha-creator/chanly/internal/post/postgres"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// start — 19 октября 2026, 10:00 по Москве; кампании — 20–22 октября.
var start = time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)

const kktu = "30.15.1"

type requisites struct{}

func (requisites) Requisites(context.Context, uuid.UUID) (postdomain.Requisites, error) {
	return postdomain.Requisites{Name: posttest.Name, INN: "7707083893"}, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type alerts struct {
	mu   sync.Mutex
	list []alert.Alert
}

func (a *alerts) Raise(_ context.Context, al alert.Alert) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, al)
	return nil
}

func (a *alerts) Resolve(context.Context, string, string) error { return nil }

func (a *alerts) has(prefix string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, al := range a.list {
		if strings.HasPrefix(al.Reason, prefix) {
			return true
		}
	}
	return false
}

// env — модули аккаунтов, каналов, post, размещений и маркировки на одной базе с поддельным ОРД.
type env struct {
	t          *testing.T
	d          *dbtest.DB
	clk        *clock.Manual
	logs       *syncBuffer
	svc        *app.Service
	store      *markingpg.Store
	ord        *ordfake.Client
	accounts   *accountapp.Service
	post       *postapp.Service
	channels   *channelapp.Service
	placements *placementapp.Service
	pstore     *placementpg.Store
	worker     *job.Worker
	alerts     *alerts
	files      *storagefake.Storage
	platform   uuid.UUID
	adv        uuid.UUID
	advUser    uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	logs := &syncBuffer{}
	log := logx.New(logs, slog.LevelDebug, "test")
	txr := tx.NewRunner(d.Pool, log)
	queue := job.NewQueue(d.Pool, clk, log)
	auditLog := audit.New(d.Pool, clk, log)
	e := &env{t: t, d: d, clk: clk, logs: logs, ord: ordfake.New(), alerts: &alerts{}, files: storagefake.New()}

	as := accountpg.New(d.Pool, log)
	e.accounts = accountapp.New(accountapp.Deps{Organizations: as.Organizations(), Platform: as.Organizations(),
		Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log})
	platform, err := e.accounts.EnsurePlatformOrganization(context.Background(), accountapp.PlatformOrg{
		INN: markingtest.PlatformINN, Name: markingtest.PlatformName, LegalType: accountdomain.SoleProprietor,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.platform = platform

	cs := channelpg.New(d.Pool, log)
	e.channels = channelapp.New(channelapp.Deps{
		Channels: cs.Channels(), LinkCodes: cs.LinkCodes(), Categories: cs.Categories(), Formats: cs.Formats(),
		Prices: cs.Prices(), Unavail: cs.Unavailabilities(), Metrics: cs.Metrics(), Catalog: cs.Catalog(),
		Admin: cs.AdminChannels(), Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log,
		Config: channelapp.Config{Pepper: []byte(strings.Repeat("p", 32)), FeeRateBP: 1500},
	})
	ps := postpg.New(d.Pool, log)
	e.post = postapp.New(postapp.Deps{
		MediaFiles: ps.MediaFiles(), Derivatives: ps.Derivatives(), Campaigns: ps.Campaigns(), Posts: ps.Posts(),
		Versions: ps.Versions(), Requisites: requisites{},
		Renderer: imaging.New(imaging.Options{PreviewSide: 640, MaxBytes: 50 << 20, Concurrency: 1}, clk, log),
		Storage:  e.files, Uploads: auditLog, Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log,
	})
	e.pstore = placementpg.New(d.Pool, log)
	mx := maxfake.New()
	mx.SetNow(clk.Now)
	e.placements = placementapp.New(placementapp.Deps{
		Carts: e.pstore.Carts(), Orders: e.pstore.Orders(), Placements: e.pstore.Placements(), History: e.pstore.History(),
		Channels: bridge.PlacementChannels{Svc: e.channels}, Campaigns: bridge.PlacementCampaigns{Svc: e.post},
		Versions: bridge.PlacementVersions{Svc: e.post},
		Holds:    placementtest.NewHolds(), Contractors: &placementtest.Contractors{}, Notifier: &placementtest.Notifier{},
		Publications: e.pstore.Publications(), Violations: e.pstore.Violations(), Disputes: e.pstore.Disputes(),
		Publisher: bridge.PlacementPublisher{API: mx}, Content: bridge.PlacementContent{Svc: e.post}, Alerts: e.alerts,
		Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log,
		Config: placementapp.Config{FeeRateBP: 1500},
	})
	link := bridge.ChannelPlacements{Svc: e.placements}
	e.channels.SetPlacementPorts(link, link, link)

	e.store = markingpg.New(d.Pool, log)
	posts := bridge.MarkingPosts{Svc: e.post}
	e.svc = app.New(app.Deps{
		Organizations: e.store.Organizations(), Contracts: e.store.Contracts(), Platforms: e.store.Platforms(),
		Creatives: e.store.Creatives(), Overview: e.store.Overview(),
		Accounts: bridge.MarkingOrganizations{Orgs: as.Organizations()}, Posts: posts, Versions: posts, Channels: e.channels,
		Ord: bridge.MarkingOrd{Client: e.ord}, Alerts: e.alerts,
		Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log,
		Config: domain.Config{KKTUCode: kktu},
	})
	e.post.SetVersionListener(bridge.VersionListeners{bridge.VersionListener{Svc: e.placements}, bridge.MarkingVersionListener{Svc: e.svc}})
	e.channels.SetMarkingPorts(bridge.ChannelApprovals{Svc: e.svc})
	e.svc.SetCreativeListener(e.placements)

	e.worker = job.NewWorker(d.Pool, clk, log)
	markingjobs.Register(e.worker, e.svc)
	placementjobs.Register(e.worker, e.placements, placementjobs.Options{})
	postjobs.Register(e.worker, e.post)
	e.adv, e.advUser = posttest.Advertiser(t, d.Pool)
	markingtest.Accept(t, d.Pool, e.adv, markingtest.AdvertiserOffer, start.Add(-48*time.Hour))
	return e
}

// run выполняет готовые задания, пока они есть.
func (e *env) run() {
	e.t.Helper()
	for range 50 {
		n, err := e.worker.RunOnce(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	e.t.Fatal("jobs did not settle")
}

// advance сдвигает часы и выполняет готовые задания.
func (e *env) advance(d time.Duration) {
	e.t.Helper()
	e.clk.Advance(d)
	e.run()
}

// settle — несколько шагов опроса по минуте, пока ОРД доводит объекты до финального статуса.
func (e *env) settle(steps int) {
	e.t.Helper()
	e.run()
	for range steps {
		e.advance(time.Minute)
	}
}

func (e *env) ctx() context.Context {
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, UserID: &e.advUser, OrganizationID: &e.adv})
}

func (e *env) org(orgID uuid.UUID) domain.Organization {
	e.t.Helper()
	o, err := e.store.Organizations().ByOrganization(context.Background(), orgID)
	if err != nil {
		e.t.Fatalf("ord organization of %s: %v", orgID, err)
	}
	return o
}

func (e *env) contract(kind domain.ContractKind, client, contractor uuid.UUID) domain.Contract {
	e.t.Helper()
	c, err := e.store.Contracts().ByParties(context.Background(), kind, client, contractor)
	if err != nil {
		e.t.Fatalf("contract %s: %v", kind, err)
	}
	return c
}

func (e *env) creative(versionID uuid.UUID) domain.Creative {
	e.t.Helper()
	c, err := e.store.Creatives().ByVersion(context.Background(), versionID)
	if err != nil {
		e.t.Fatalf("creative of %s: %v", versionID, err)
	}
	return c
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// jobStatus — состояние последнего задания по dedupe_key; пусто — нет.
func (e *env) jobStatus(key string) string {
	var st string
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT status FROM job WHERE dedupe_key = $1 ORDER BY created_at DESC LIMIT 1`,
		key).Scan(&st); err != nil {
		return ""
	}
	return st
}

// logged — записи журнала с сообщением msg.
func (e *env) logged(msg string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(e.logs.String(), "\n") {
		if !strings.Contains(line, `"msg":"`+msg+`"`) {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// ---------------------------------------------------------------- заказ и версия

// order — заказ рекламодателя на канал с текстовым постом; версия в pending_moderation.
func (e *env) order(channels ...uuid.UUID) placementapp.OrderView {
	e.t.Helper()
	for _, c := range channels {
		if _, err := e.placements.AddCartItem(e.ctx(), e.adv, e.advUser, c, "f_1_24"); err != nil {
			e.t.Fatal(err)
		}
	}
	name := "Кампания"
	from, to := postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22)
	tf, tt := postdomain.TimeOfDay(9*60), postdomain.TimeOfDay(21*60)
	c, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{
		Name: &name, DateFrom: &from, DateTo: &to, TimeFrom: &tf, TimeTo: &tt,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	carrier := postdomain.CarrierText
	doc := postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{
		{Text: "Скидки "}, {Text: "в магазине", Marks: []postdomain.Mark{{Type: postdomain.MarkLink, Href: "https://shop.example.test/sale"}}},
	}}}}
	if _, err := e.post.SaveDraft(e.ctx(), e.adv, e.advUser, c.ID, postapp.DraftInput{Doc: doc, TokenCarrier: &carrier}); err != nil {
		e.t.Fatal(err)
	}
	o, err := e.placements.Checkout(e.ctx(), e.adv, e.advUser, c.ID, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return e.getOrder(o.ID)
}

func (e *env) getOrder(id uuid.UUID) placementapp.OrderView {
	e.t.Helper()
	v, err := e.placements.GetOrder(context.Background(), e.adv, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// approve — модератор одобрил версию заказа (S-17).
func (e *env) approve(versionID uuid.UUID) {
	e.t.Helper()
	if _, err := e.post.ApproveVersion(context.Background(), versionID, e.advUser); err != nil {
		e.t.Fatal(err)
	}
}

// blogger — блогер с активным каналом и принятой офертой блогера.
func (e *env) blogger(title string) (org, channel uuid.UUID) {
	e.t.Helper()
	org, _ = placementtest.Blogger(e.t, e.d.Pool)
	markingtest.Accept(e.t, e.d.Pool, org, markingtest.BloggerOffer, start.Add(-24*time.Hour))
	return org, placementtest.Channel(e.t, e.d.Pool, org, title, 1_000_000)
}
