package httpapi_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/httpapi"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

const path = "/api/v1/webhooks/rocketwork"

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

func (a *alerts) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.list)
}

// parser — app.Service.ParseWebhook без сервиса: режим и адаптер.
type parser struct {
	enabled bool
	p       bridge.PayoutProvider
}

func (p parser) ParseWebhook(h map[string][]string, body []byte) (domain.WebhookEvent, error) {
	if !p.enabled {
		return domain.WebhookEvent{}, domain.ErrProviderDisabled
	}
	return p.p.ParseWebhook(h, body)
}

type server struct {
	srv  *httptest.Server
	d    *dbtest.DB
	fake *rocketworkfake.Fake
	al   *alerts
	logs *bytes.Buffer
}

func newServer(t *testing.T, enabled bool) *server {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	logs := &bytes.Buffer{}
	log := logx.New(logs, slog.LevelDebug, "test")
	fake := rocketworkfake.New([]byte("webhook-secret-0123456789"))
	rec := inbound.NewRecorder(d.Pool, tx.NewRunner(d.Pool, log), job.NewQueue(d.Pool, clk, log), clk, log)
	al := &alerts{}
	h := httpapi.NewWebhook(parser{enabled: enabled, p: bridge.PayoutProvider{API: fake, Log: log}}, rec, al, clk, log, nil)
	mux := http.NewServeMux()
	mux.Handle("POST "+path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &server{srv: srv, d: d, fake: fake, al: al, logs: logs}
}

func (s *server) post(t *testing.T, h http.Header, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.srv.URL+path, bytes.NewReader(body))
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (s *server) count(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := s.d.Pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWebhookAccepted(t *testing.T) {
	s := newServer(t, true)
	h, body := s.fake.SignedWebhook(rocketwork.EventPaymentStatus, "rwp-1")
	for range 2 {
		if code := s.post(t, h, body); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	if n := s.count(t, `SELECT count(*) FROM inbound_event WHERE source = 'rocketwork' AND event_type = 'payout_status'
		AND payload->>'external_id' = 'rwp-1'`); n != 1 {
		t.Fatalf("inbound events %d", n)
	}
	if n := s.count(t, `SELECT count(*) FROM job WHERE kind = 'process_inbound_event'`); n != 1 {
		t.Fatalf("jobs %d", n)
	}
	h, body = s.fake.SignedWebhook(rocketwork.EventContractorStatus, "rwc-1")
	if code := s.post(t, h, body); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if n := s.count(t, `SELECT count(*) FROM inbound_event WHERE event_type = 'contractor_status'`); n != 1 {
		t.Fatalf("contractor events %d", n)
	}
	if !strings.Contains(s.logs.String(), "rocketwork webhook received") {
		t.Fatal("not logged")
	}
}

func TestWebhookRejected(t *testing.T) {
	s := newServer(t, true)
	h, body := s.fake.SignedWebhook(rocketwork.EventPaymentStatus, "rwp-1")
	h.Set(rocketwork.SignatureHeader, "sha256=00")
	for i := range 5 {
		if code := s.post(t, h, body); code != http.StatusUnauthorized {
			t.Fatalf("status %d", code)
		}
		if want := map[bool]int{true: 1, false: 0}[i == 4]; s.al.count() != want {
			t.Fatalf("after %d failures alerts %d", i+1, s.al.count())
		}
	}
	h.Del(rocketwork.SignatureHeader)
	if code := s.post(t, h, body); code != http.StatusUnauthorized {
		t.Fatalf("no signature: %d", code)
	}
	if n := s.count(t, `SELECT count(*) FROM inbound_event`); n != 0 {
		t.Fatalf("events %d", n)
	}
	if !strings.Contains(s.logs.String(), "rocketwork webhook rejected") {
		t.Fatal("not logged")
	}
}

func TestWebhookLimitsAndFormat(t *testing.T) {
	s := newServer(t, true)
	big := bytes.Repeat([]byte("a"), httpapi.WebhookBodyLimit+1)
	if code := s.post(t, http.Header{}, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d", code)
	}
	// Подпись верна, формат нет — 200 без события.
	body := []byte(`{"id":"e1","event":"unknown.event","object_id":"x"}`)
	h := http.Header{}
	h.Set(rocketwork.SignatureHeader, rocketwork.Sign(s.fake.Secret(), body))
	if code := s.post(t, h, body); code != http.StatusOK {
		t.Fatalf("malformed: %d", code)
	}
	if n := s.count(t, `SELECT count(*) FROM inbound_event`); n != 0 {
		t.Fatalf("events %d", n)
	}
}

func TestWebhookDisabled(t *testing.T) {
	s := newServer(t, false)
	h, body := s.fake.SignedWebhook(rocketwork.EventPaymentStatus, "rwp-1")
	if code := s.post(t, h, body); code != http.StatusNotFound {
		t.Fatalf("disabled: %d", code)
	}
}
