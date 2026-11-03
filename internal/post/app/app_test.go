package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/post/app"
	"github.com/leha-creator/chanly/internal/post/domain"
	"github.com/leha-creator/chanly/internal/post/imaging"
	"github.com/leha-creator/chanly/internal/post/jobs"
	"github.com/leha-creator/chanly/internal/post/postgres"
	"github.com/leha-creator/chanly/internal/post/posttest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// start — 2026-10-16 12:00 по Москве.
var start = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// requisites — реквизиты организаций для плашки (R-14).
type requisites struct {
	mu  sync.Mutex
	org map[uuid.UUID]domain.Requisites
}

func (r *requisites) Requisites(_ context.Context, orgID uuid.UUID) (domain.Requisites, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.org[orgID], nil
}

func (r *requisites) set(orgID uuid.UUID, name, inn string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.org[orgID] = domain.Requisites{Name: name, INN: inn}
}

type env struct {
	t      *testing.T
	d      *dbtest.DB
	clk    *clock.Manual
	svc    *app.Service
	store  *postgres.Store
	files  *storagefake.Storage
	req    *requisites
	worker *job.Worker
	logs   *syncBuffer
	org    uuid.UUID
	user   uuid.UUID
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

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(start)
	logs := &syncBuffer{}
	log := logx.New(logs, slog.LevelDebug, "test")
	st := postgres.New(d.Pool, log)
	auditLog := audit.New(d.Pool, clk, log)
	e := &env{t: t, d: d, clk: clk, store: st, files: storagefake.New(), logs: logs,
		req: &requisites{org: map[uuid.UUID]domain.Requisites{}}}
	e.svc = app.New(app.Deps{
		MediaFiles: st.MediaFiles(), Derivatives: st.Derivatives(), Campaigns: st.Campaigns(), Posts: st.Posts(),
		Versions: st.Versions(), Requisites: e.req,
		Renderer: imaging.New(imaging.Options{PreviewSide: 640, MaxBytes: 50 << 20, Concurrency: 2}, clk, log),
		Storage:  e.files, Uploads: auditLog, Tx: tx.NewRunner(d.Pool, log), Jobs: job.NewQueue(d.Pool, clk, log),
		Audit: auditLog, Clock: clk, Log: log,
	})
	e.worker = job.NewWorker(d.Pool, clk, log)
	jobs.Register(e.worker, e.svc)
	e.org, e.user = posttest.Advertiser(t, d.Pool)
	e.req.set(e.org, posttest.Name, "7707083893")
	return e
}

// ctx — действия пользователя рекламодателя организации env.
func (e *env) ctx() context.Context {
	return audit.WithActor(context.Background(), audit.Actor{Type: audit.ActorAdvertiser, UserID: &e.user, OrganizationID: &e.org})
}

// drain выполняет все готовые задания.
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

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) upload(data []byte) app.MediaView {
	e.t.Helper()
	v, err := e.svc.Upload(e.ctx(), e.org, e.user, "photo.jpg", data)
	if err != nil {
		e.t.Fatalf("upload: %v", err)
	}
	return v
}

func wantReason(t *testing.T, err error, code apperr.Code, reason string) {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	if reason != "" && e.Details["reason"] != reason {
		t.Fatalf("want reason %q, got %v", reason, e.Details)
	}
}

// noSecrets проверяет, что журнал не содержит текста поста, адресов, реквизитов и имени файла (07 §5.4).
func (e *env) noSecrets(values ...string) {
	e.t.Helper()
	logs := e.logs.String()
	for _, v := range values {
		if strings.Contains(logs, v) {
			e.t.Fatalf("log contains %q", v)
		}
	}
}

func doc(paragraphs ...string) domain.Doc {
	var d domain.Doc
	for _, p := range paragraphs {
		d.Blocks = append(d.Blocks, domain.Block{Type: domain.BlockParagraph, Content: []domain.Text{{Text: p}}})
	}
	return d
}

func draftInput(d domain.Doc, media ...uuid.UUID) app.DraftInput {
	return app.DraftInput{Doc: d, MediaIDs: media}
}

func (e *env) campaign(name string) domain.Campaign {
	e.t.Helper()
	c, err := e.svc.CreateCampaign(e.ctx(), e.org, e.user, domain.CampaignInput{Name: &name})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func mustUUID(s string) uuid.UUID { return uuid.MustParse(s) }

func buildPayload(t *testing.T, derivativeID string, org uuid.UUID) app.BuildPayload {
	t.Helper()
	return app.BuildPayload{DerivativeID: uuid.MustParse(derivativeID), OrganizationID: org}
}

func ptr[T any](v T) *T { return &v }

func codes(ps []domain.Problem) []string { return domain.Codes(ps) }
