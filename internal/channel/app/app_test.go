package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/jobs"
	"github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// fakeMAX — MAX для сценариев: сведения о каналах, права, удалённые сообщения.
type fakeMAX struct {
	mu      sync.Mutex
	chats   map[int64]domain.ChatInfo
	members map[int64]domain.Membership
	deleted []string
	left    []int64
	sent    map[int64][]string
	fail    error

	messages     map[int64][]domain.MessageStat
	failMessages error
	listCalls    int
}

func newFakeMAX() *fakeMAX {
	return &fakeMAX{chats: map[int64]domain.ChatInfo{}, members: map[int64]domain.Membership{}, sent: map[int64][]string{},
		messages: map[int64][]domain.MessageStat{}}
}

func (f *fakeMAX) set(chatID int64, info domain.ChatInfo, m domain.Membership) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats[chatID], f.members[chatID] = info, m
}

func (f *fakeMAX) GetChat(_ context.Context, id int64) (domain.ChatInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return domain.ChatInfo{}, f.fail
	}
	c, ok := f.chats[id]
	if !ok {
		return domain.ChatInfo{}, job.Permanent(fmt.Errorf("chat %d not found", id))
	}
	return c, nil
}

func (f *fakeMAX) GetMembership(_ context.Context, id int64) (domain.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[id], f.fail
}

func (f *fakeMAX) DeleteMessage(_ context.Context, _ int64, mid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, mid)
	return f.fail
}

func (f *fakeMAX) LeaveChat(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = append(f.left, id)
	return f.fail
}

func (f *fakeMAX) SendUserMessage(_ context.Context, userID int64, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent[userID] = append(f.sent[userID], text)
	return "mid.reply", f.fail
}

// alerts — пойманные оповещения.
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

// availability — пойманные вызовы порта размещений.
type availability struct {
	mu       sync.Mutex
	lost     []domain.StatusChange
	restored []domain.StatusChange
}

func (a *availability) ChannelUnavailable(_ context.Context, c domain.StatusChange) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lost = append(a.lost, c)
	return nil
}

func (a *availability) ChannelRestored(_ context.Context, c domain.StatusChange) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.restored = append(a.restored, c)
	return nil
}

type env struct {
	t        *testing.T
	d        *dbtest.DB
	clk      *clock.Manual
	svc      *app.Service
	max      *fakeMAX
	alerts   *alerts
	avail    *availability
	store    *postgres.Store
	worker   *job.Worker
	recorder *inbound.Recorder
	logs     *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	logs := &bytes.Buffer{}
	log := logx.New(&syncWriter{w: logs}, slog.LevelDebug, "test")
	st := postgres.New(d.Pool, log)
	queue := job.NewQueue(d.Pool, clk, log)
	txr := tx.NewRunner(d.Pool, log)
	e := &env{t: t, d: d, clk: clk, max: newFakeMAX(), alerts: &alerts{}, avail: &availability{}, store: st, logs: logs}
	e.svc = app.New(app.Deps{
		Channels: st.Channels(), LinkCodes: st.LinkCodes(), Categories: st.Categories(),
		Formats: st.Formats(), Prices: st.Prices(), Unavail: st.Unavailabilities(), Metrics: st.Metrics(),
		Catalog: st.Catalog(), Admin: st.AdminChannels(), Messages: e.max,
		Chats: e.max, Bot: e.max, Availability: e.avail, Alerts: e.alerts,
		Tx: txr, Jobs: queue, Audit: audit.New(d.Pool, clk, log), Clock: clk, Log: log,
		Config: app.Config{Pepper: []byte(strings.Repeat("p", 32)), BotName: "@maxim_test_bot", FeeRateBP: 1500},
	})
	e.worker = job.NewWorker(d.Pool, clk, log)
	dispatcher := inbound.NewDispatcher(d.Pool, clk, log)
	jobs.Register(e.worker, dispatcher, e.svc)
	dispatcher.Register(e.worker)
	e.recorder = inbound.NewRecorder(d.Pool, txr, queue, clk, log)
	return e
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (e *env) ctx() context.Context {
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorBlogger})
}

// drain выполняет все готовые задания, сдвигая часы на секунду между проходами.
func (e *env) drain() {
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

// event записывает событие MAX так, как это делает приём Webhook.
func (e *env) event(body string) {
	e.t.Helper()
	var head struct {
		UpdateType string `json:"update_type"`
	}
	if err := json.Unmarshal([]byte(body), &head); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.recorder.Record(context.Background(), inbound.Event{
		Source: inbound.SourceMAX, ExternalKey: inbound.KeyFromBody([]byte(body)), EventType: head.UpdateType, Payload: []byte(body),
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) channel(chatID int64) *domain.Channel {
	e.t.Helper()
	var id uuid.UUID
	if err := e.d.Pool.QueryRow(context.Background(), `SELECT id FROM channel WHERE max_chat_id = $1`, chatID).Scan(&id); err != nil {
		e.t.Fatalf("channel %d: %v", chatID, err)
	}
	c, err := e.store.Channels().GetForUpdate(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
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

func (e *env) org() uuid.UUID { return channeltest.Organization(e.t, e.d.Pool) }

var (
	allRights = domain.Membership{IsAdmin: true, Permissions: []string{"write", "delete", "edit"}}
	publicCh  = domain.ChatInfo{Title: "Рецепты", Link: "https://max.ru/recipes", IsPublic: true, IsChannel: true, SubscribersCount: 4200}
)

func botAdded(chatID, userID int64) string {
	return fmt.Sprintf(`{"update_type":"bot_added","timestamp":%d,"chat_id":%d,"user":{"user_id":%d,"name":"Анна"},"is_channel":true}`,
		time.Now().UnixNano(), chatID, userID)
}

func botRemoved(chatID int64) string {
	return fmt.Sprintf(`{"update_type":"bot_removed","timestamp":%d,"chat_id":%d,"user":{"user_id":1},"is_channel":true}`, time.Now().UnixNano(), chatID)
}

func rightsChanged(chatID int64) string {
	return fmt.Sprintf(`{"update_type":"bot_admin_permissions_changed","timestamp":%d,"chat_id":%d,"user":{"user_id":1},"is_channel":true}`,
		time.Now().UnixNano(), chatID)
}

func channelPost(chatID int64, mid, text string) string {
	return fmt.Sprintf(`{"update_type":"message_created","timestamp":%d,"message":{"recipient":{"chat_id":%d,"chat_type":"channel"},"body":{"mid":%q,"text":%q}}}`,
		time.Now().UnixNano(), chatID, mid, text)
}

func dialogMessage(userID int64, text string) string {
	return fmt.Sprintf(`{"update_type":"message_created","timestamp":%d,"message":{"sender":{"user_id":%d},"recipient":{"chat_id":900,"chat_type":"dialog"},"body":{"mid":"mid.d","text":%q}}}`,
		time.Now().UnixNano(), userID, text)
}

func contains[T comparable](s []T, v T) bool { return slices.Contains(s, v) }

// messages — сообщения каналов для ListMessages; failMessages — ошибка только этого метода.
func (f *fakeMAX) ListMessages(_ context.Context, chatID int64, from, to time.Time, count int) ([]domain.MessageStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.failMessages != nil {
		return nil, f.failMessages
	}
	out := []domain.MessageStat{}
	for _, m := range f.messages[chatID] {
		if !m.At.Before(from) && !m.At.After(to) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b domain.MessageStat) int { return b.At.Compare(a.At) })
	if len(out) > count {
		out = out[:count]
	}
	return out, nil
}
