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

	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/placement/app"
	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/placement/jobs"
	"github.com/leha-creator/chanly/internal/placement/placementtest"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
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

// start — 19 октября 2026, 10:00 по Москве; кампании — 20–22 октября, 09:00–21:00.
var start = time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)

// msk — момент по Москве.
func msk(d, hh, mm int) time.Time {
	return time.Date(2026, 10, d, hh, mm, 0, 0, time.FixedZone("MSK", 3*3600)).UTC()
}

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

// env — модули каналов, post и размещений на одной базе с поддельными портами будущих модулей.
type env struct {
	t           *testing.T
	d           *dbtest.DB
	clk         *clock.Manual
	logs        *syncBuffer
	svc         *app.Service
	post        *postapp.Service
	channels    *channelapp.Service
	store       *placementpg.Store
	holds       *placementtest.Holds
	notices     *placementtest.Notifier
	contractors *placementtest.Contractors
	payouts     *placementtest.Payouts
	worker      *job.Worker
	dispatcher  *inbound.Dispatcher
	recorder    *inbound.Recorder
	max         *maxfake.Client
	alerts      *alerts
	files       *storagefake.Storage
	tx          tx.Runner
	adv         uuid.UUID
	advUser     uuid.UUID
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
	e := &env{t: t, d: d, clk: clk, logs: logs, holds: placementtest.NewHolds(), notices: &placementtest.Notifier{},
		contractors: &placementtest.Contractors{}, payouts: &placementtest.Payouts{}, tx: txr, max: maxfake.New(), alerts: &alerts{}, files: storagefake.New()}
	e.max.SetNow(clk.Now)

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
	e.store = placementpg.New(d.Pool, log)
	e.svc = app.New(app.Deps{
		Carts: e.store.Carts(), Orders: e.store.Orders(), Placements: e.store.Placements(), History: e.store.History(),
		Channels: bridge.PlacementChannels{Svc: e.channels}, Campaigns: bridge.PlacementCampaigns{Svc: e.post},
		Versions: bridge.PlacementVersions{Svc: e.post},
		Holds:    e.holds, Contractors: e.contractors, Notifier: e.notices, Payouts: e.payouts,
		Publications: e.store.Publications(), Violations: e.store.Violations(), Disputes: e.store.Disputes(),
		Claims: e.store.Claims(), Ratings: bridge.PlacementChannelRatings{Svc: e.channels},
		Publisher: bridge.PlacementPublisher{API: e.max}, Content: bridge.PlacementContent{Svc: e.post}, Alerts: e.alerts,
		Tx: txr, Jobs: queue, Audit: auditLog, Clock: clk, Log: log,
		Config: app.Config{FeeRateBP: 1500, Control: domain.ControlRules{RatingPenaltyRefund: 10, RatingPenaltyProportional: 5}},
	})
	e.post.SetVersionListener(bridge.VersionListener{Svc: e.svc})
	link := bridge.ChannelPlacements{Svc: e.svc}
	e.channels.SetPlacementPorts(link, link, link)
	e.channels.SetPostListener(bridge.ChannelPosts{Svc: e.svc})
	e.worker = job.NewWorker(d.Pool, clk, log)
	jobs.Register(e.worker, e.svc, jobs.Options{})
	postjobs.Register(e.worker, e.post)
	e.dispatcher = inbound.NewDispatcher(d.Pool, clk, log)
	jobs.RegisterEvents(e.dispatcher, e.svc)
	e.dispatcher.Register(e.worker)
	e.recorder = inbound.NewRecorder(d.Pool, txr, queue, clk, log)
	e.adv, e.advUser = posttest.Advertiser(t, d.Pool)
	return e
}

// ctx — действия пользователя рекламодателя.
func (e *env) ctx() context.Context {
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, UserID: &e.advUser, OrganizationID: &e.adv})
}

// blogger — блогер с активным каналом и ценой f_1_24.
func (e *env) blogger(title string, amount money.Kopecks) (org, user, channel uuid.UUID) {
	e.t.Helper()
	org, user = placementtest.Blogger(e.t, e.d.Pool)
	return org, user, placementtest.Channel(e.t, e.d.Pool, org, title, amount)
}

// campaign — кампания 20–22 октября 09:00–21:00 МСК с текстовым постом без проблем.
func (e *env) campaign(name string) uuid.UUID {
	e.t.Helper()
	from, to := postdomain.NewDate(2026, 10, 20), postdomain.NewDate(2026, 10, 22)
	tf, tt := postdomain.TimeOfDay(9*60), postdomain.TimeOfDay(21*60)
	c, err := e.post.CreateCampaign(e.ctx(), e.adv, e.advUser, postdomain.CampaignInput{
		Name: &name, DateFrom: &from, DateTo: &to, TimeFrom: &tf, TimeTo: &tt,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.saveDraft(c.ID, "Скидки https://shop.example.test/sale")
	return c.ID
}

func (e *env) saveDraft(campaignID uuid.UUID, text string) {
	e.t.Helper()
	carrier := postdomain.CarrierText
	doc := postdomain.Doc{Blocks: []postdomain.Block{{Type: postdomain.BlockParagraph, Content: []postdomain.Text{{Text: text}}}}}
	if _, err := e.post.SaveDraft(e.ctx(), e.adv, e.advUser, campaignID, postapp.DraftInput{Doc: doc, TokenCarrier: &carrier}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) addToCart(channel uuid.UUID) {
	e.t.Helper()
	if _, err := e.svc.AddCartItem(e.ctx(), e.adv, e.advUser, channel, "f_1_24"); err != nil {
		e.t.Fatal(err)
	}
}

// order — оформленный заказ из каналов (корзина собирается здесь).
func (e *env) order(channels ...uuid.UUID) app.OrderView {
	e.t.Helper()
	for _, c := range channels {
		e.addToCart(c)
	}
	o, err := e.svc.Checkout(e.ctx(), e.adv, e.advUser, e.campaign("Кампания"), "")
	if err != nil {
		e.t.Fatal(err)
	}
	return e.getOrder(o.ID)
}

func (e *env) getOrder(id uuid.UUID) app.OrderView {
	e.t.Helper()
	v, err := e.svc.GetOrder(context.Background(), e.adv, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) admin() uuid.UUID { return e.advUser }

// approve и ready двигают текущую версию заказа так, как это сделают модерация и маркировка.
func (e *env) approve(o app.OrderView) {
	e.t.Helper()
	if _, err := e.post.ApproveVersion(context.Background(), o.Version.ID, e.admin()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ready(o app.OrderView) {
	e.t.Helper()
	e.approve(o)
	if _, err := e.post.MarkVersionReady(context.Background(), o.Version.ID, "E-"+o.Version.ID.String()[:8]); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) placement(id uuid.UUID) domain.Placement {
	e.t.Helper()
	p, err := e.store.Placements().Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// jobStatus — состояние живого задания по dedupe_key; пусто — нет.
func (e *env) jobStatus(key string) string {
	e.t.Helper()
	var st string
	err := e.d.Pool.QueryRow(context.Background(), `SELECT status FROM job WHERE dedupe_key = $1 ORDER BY created_at DESC LIMIT 1`, key).Scan(&st)
	if err != nil {
		return ""
	}
	return st
}

// jobRunAt — время запуска ожидающего задания.
func (e *env) jobRunAt(key string) time.Time {
	e.t.Helper()
	var at time.Time
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT run_at FROM job WHERE dedupe_key = $1 AND status = 'pending'`, key).Scan(&at); err != nil {
		e.t.Fatalf("job %s: %v", key, err)
	}
	return at
}

// runJobs выполняет готовые задания.
func (e *env) runJobs() {
	e.t.Helper()
	for range 20 {
		n, err := e.worker.RunOnce(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func wantReason(t *testing.T, err error, code apperr.Code, reason string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	if reason != "" && ae.Details["reason"] != reason {
		t.Fatalf("want reason %q, got %v", reason, ae.Details)
	}
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

// alerts — запись оповещений администратору.
type alerts struct {
	mu       sync.Mutex
	list     []alert.Alert
	resolved []string
}

func (a *alerts) Raise(_ context.Context, al alert.Alert) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, al)
	return nil
}

func (a *alerts) Resolve(_ context.Context, reason, objectKey string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolved = append(a.resolved, reason+":"+objectKey)
	return nil
}

// reasons — причины оповещений по порядку.
func (a *alerts) reasons() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.list))
	for _, al := range a.list {
		out = append(out, al.Reason)
	}
	return out
}
