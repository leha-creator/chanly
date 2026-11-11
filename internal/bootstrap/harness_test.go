package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/mail/mailfake"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/integration/ord/ordfake"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
	"github.com/leha-creator/chanly/internal/platform/secretbox"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var testStart = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// Секреты Webhook MAX тестового приложения.
const (
	testWebhookPath   = "test-webhook-path-secret-0123456789abcdef"
	testWebhookSecret = "test-webhook-header-secret-0123456789abcd"
)

// testApp — собранный сервер и обработчик заданий в одном процессе:
// настоящая PostgreSQL, поддельные почта и хранилище, часы с ручным ходом.
type testApp struct {
	t      *testing.T
	db     *dbtest.DB
	clk    *clock.Manual
	srv    *httptest.Server
	worker *job.Worker
	mail   *mailfake.Sender
	files  *storagefake.Storage
	mods   *modules
	ord    *ordfake.Client
	router routers.Router
	logs   *bytes.Buffer
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	return newTestAppWith(t, testConfig())
}

// newTestAppWith — то же с заданной конфигурацией (например, ORD_ENABLED=false).
func newTestAppWith(t *testing.T, cfg Config) *testApp {
	t.Helper()
	d := dbtest.New(t)
	logs := &bytes.Buffer{}
	log := logx.New(&lockedWriter{w: logs}, slog.LevelDebug, "api")
	box, err := secretbox.New(bytes.Repeat([]byte{3}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewManual(testStart)
	fake := maxfake.New()
	fake.SetNow(clk.Now)
	ordFake := ordfake.New()
	var rwOpts []rocketworkfake.Option
	if cfg.RocketWork.FakeStatePath != "" { // общее состояние с командами maxim dev rocketwork
		rwOpts = append(rwOpts, rocketworkfake.WithStatePath(cfg.RocketWork.FakeStatePath))
	}
	rwFake := rocketworkfake.New(cfg.RocketWorkWebhookSecret(), rwOpts...)
	in := &infra{pool: d.Pool, clock: clk, log: log, storage: storagefake.New(), mail: mailfake.New(), max: fake, box: box, ord: ordFake,
		rocketwork: rwFake}
	in.alerter = newAlerter(cfg, in)
	m := newModules(cfg, in)
	if err := m.startMarking(context.Background()); err != nil {
		t.Fatal(err)
	}
	streamCtx, stopStreams := context.WithCancel(context.Background())
	t.Cleanup(stopStreams)
	h, err := apiHandler(streamCtx, cfg, in, m, metrics.New())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { stopStreams(); srv.Close() })

	spec, err := loadSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{
		t: t, db: d, clk: clk, srv: srv, worker: newWorker(in, m), mail: in.mail.(*mailfake.Sender),
		files: in.storage.(*storagefake.Storage), mods: m, router: router, logs: logs, ord: ordFake,
	}
}

func testConfig() Config {
	cfg, err := LoadConfig(map[string]string{
		"APP_ENV": "test", "DATABASE_URL": "postgres://unused", "APP_SECRETS_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
		"AUTH_CODE_PEPPER": strings.Repeat("x", 32), "S3_ENDPOINT": "unused", "S3_BUCKET_PRIVATE": "b",
		"S3_ACCESS_KEY": "a", "S3_SECRET_KEY": "s",
		"MAX_WEBHOOK_PATH_SECRET": testWebhookPath, "MAX_WEBHOOK_SECRET": testWebhookSecret, "MAX_BOT_NAME": "@maxim_test_bot",
	}, ModeAPI)
	if err != nil {
		panic(err)
	}
	return cfg
}

// client — пользователь с собственными cookie. Меняющие запросы несут X-CSRF-Token.
type client struct {
	app  *testApp
	http *http.Client
	csrf bool
}

func (a *testApp) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{app: a, http: &http.Client{Jar: jar}, csrf: true}
}

type response struct {
	status int
	header http.Header
	body   []byte
	req    *http.Request
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r response) errorCode(t *testing.T) (string, map[string]any) {
	t.Helper()
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	r.json(t, &e)
	return e.Error.Code, e.Error.Details
}

func (c *client) do(method, path string, body any) response {
	c.app.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case multipartBody:
		rd, ct = bytes.NewReader(b.data), b.contentType
	default:
		raw, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(raw), "application/json"
	}
	req, _ := http.NewRequest(method, c.app.srv.URL+"/api/v1"+path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c.csrf && method != http.MethodGet {
		u, _ := url.Parse(c.app.srv.URL)
		for _, ck := range c.http.Jar.Cookies(u) {
			if ck.Name == "csrf_token" {
				req.Header.Set("X-CSRF-Token", ck.Value)
			}
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.app.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := response{status: resp.StatusCode, header: resp.Header, body: raw, req: req}
	c.app.checkResponse(r)
	return r
}

// checkResponse сверяет ответ со спецификацией: статус описан, тело по схеме.
func (a *testApp) checkResponse(r response) {
	a.t.Helper()
	// 501 описан в спецификации общим ответом NotImplemented (R-7), а не в каждой операции.
	if r.status == http.StatusNotImplemented {
		return
	}
	if r.status == http.StatusNotFound && strings.Contains(string(r.body), "no such API operation") {
		return
	}
	req := r.req.Clone(context.Background())
	req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/v1")
	route, params, err := a.router.FindRoute(req)
	if err != nil {
		a.t.Fatalf("route %s %s: %v", req.Method, req.URL.Path, err)
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}},
		Status: r.status, Header: r.header, Body: io.NopCloser(bytes.NewReader(r.body)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		a.t.Fatalf("%s %s: response %d does not match the spec: %v\n%s", req.Method, req.URL.Path, r.status, err, r.body)
	}
}

type multipartBody struct {
	data        []byte
	contentType string
}

func fileBody(name string, data []byte) multipartBody {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, _ := w.CreateFormFile("file", name)
	_, _ = part.Write(data)
	_ = w.Close()
	return multipartBody{data: b.Bytes(), contentType: w.FormDataContentType()}
}

// login проходит вход по коду из письма и возвращает тело Me.
func (c *client) login(email string) map[string]any {
	c.app.t.Helper()
	if r := c.do(http.MethodPost, "/auth/code", map[string]string{"email": email}); r.status != http.StatusAccepted {
		c.app.t.Fatalf("request code: %d %s", r.status, r.body)
	}
	// Письмо с кодом — задание; впереди в очереди могут быть другие (уведомления).
	before := c.app.mail.Count()
	for i := 0; i < 20 && c.app.mail.Count() == before; i++ {
		if _, err := c.app.worker.RunOnce(context.Background()); err != nil {
			c.app.t.Fatal(err)
		}
	}
	msg, ok := c.app.mail.Last(domain.NormalizeEmail(email))
	if !ok {
		c.app.t.Fatalf("no mail to %s", email)
	}
	r := c.do(http.MethodPost, "/auth/login", map[string]string{"email": email, "code": msg.Code})
	if r.status != http.StatusOK {
		c.app.t.Fatalf("login: %d %s", r.status, r.body)
	}
	var me map[string]any
	r.json(c.app.t, &me)
	return me
}

// publishAll публикует первую версию каждого вида документа.
func (a *testApp) publishAll() map[domain.LegalDocumentKind]string {
	a.t.Helper()
	out := map[domain.LegalDocumentKind]string{}
	for _, k := range domain.LegalDocumentKinds {
		d, err := a.mods.account.PublishLegalDocument(context.Background(), k, []byte("%PDF-1.7 "+string(k)), testStart)
		if err != nil {
			a.t.Fatal(err)
		}
		out[k] = d.ID.String()
	}
	return out
}

// lockedWriter — буфер журнала, безопасный для конкурентной записи.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// ordSettle — worker регистрирует в ordfake созданные объекты ОРД: отправка, затем опросы
// статуса раз в минуту (этап «Маркировка»). Одобренный канал после этого виден в каталоге.
func (a *testApp) ordSettle() {
	a.t.Helper()
	a.settle()
	for range 4 {
		a.clk.Advance(time.Minute)
		a.settle()
	}
}
