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

	"github.com/leha-creator/chanly/internal/channel/httpapi"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

const (
	pathSecret   = "path-secret-0123456789abcdef0123456789"
	headerSecret = "header-secret-0123456789abcdef01234567"
	header       = "X-Max-Bot-Api-Secret"
	body         = `{"update_type":"bot_added","timestamp":1760600000000,"chat_id":-70000000001,"user":{"user_id":500100},"is_channel":true}`
)

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

func newServer(t *testing.T) (*httptest.Server, *dbtest.DB, *alerts, *bytes.Buffer) {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	logs := &bytes.Buffer{}
	log := logx.New(logs, slog.LevelDebug, "test")
	rec := inbound.NewRecorder(d.Pool, tx.NewRunner(d.Pool, log), job.NewQueue(d.Pool, clk, log), clk, log)
	al := &alerts{}
	h := httpapi.NewWebhook(rec, al, clk, log, nil, httpapi.WebhookOptions{
		PathSecret: pathSecret, HeaderSecret: headerSecret, SecretHeader: header,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/webhooks/max/{secret}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, d, al, logs
}

func post(t *testing.T, srv *httptest.Server, path, secret, payload string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/webhooks/max/"+path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(header, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func count(t *testing.T, d *dbtest.DB, q string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWebhookAccepted(t *testing.T) {
	srv, d, _, logs := newServer(t)
	for range 2 {
		if code := post(t, srv, pathSecret, headerSecret, body); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM inbound_event WHERE source = 'max' AND event_type = 'bot_added'`); n != 1 {
		t.Fatalf("inbound events = %d", n)
	}
	if n := count(t, d, `SELECT count(*) FROM job WHERE kind = 'process_inbound_event'`); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	if strings.Contains(logs.String(), pathSecret) || strings.Contains(logs.String(), headerSecret) {
		t.Fatal("webhook secret written to the log")
	}
}

func TestWebhookRejected(t *testing.T) {
	srv, d, al, logs := newServer(t)
	if code := post(t, srv, "wrong", headerSecret, body); code != http.StatusUnauthorized {
		t.Fatalf("wrong path: %d", code)
	}
	if code := post(t, srv, pathSecret, "wrong", body); code != http.StatusUnauthorized {
		t.Fatalf("wrong header: %d", code)
	}
	if code := post(t, srv, pathSecret, "", body); code != http.StatusUnauthorized {
		t.Fatalf("no header: %d", code)
	}
	if n := count(t, d, `SELECT count(*) FROM inbound_event`); n != 0 {
		t.Fatalf("rejected events stored: %d", n)
	}
	if len(al.list) != 0 {
		t.Fatal("alert after 3 failures")
	}
	for range 2 {
		post(t, srv, "wrong", "", body)
	}
	if len(al.list) == 0 || al.list[0].Reason != "webhook_auth_failed:max" {
		t.Fatalf("alerts %+v", al.list)
	}
	if !strings.Contains(logs.String(), `"msg":"inbound event rejected"`) {
		t.Fatal("rejection not logged")
	}
}

func TestWebhookBodyLimitAndMalformed(t *testing.T) {
	srv, d, _, _ := newServer(t)
	big := `{"update_type":"message_created","pad":"` + strings.Repeat("x", httpapi.WebhookBodyLimit) + `"}`
	if code := post(t, srv, pathSecret, headerSecret, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MB + 1: %d", code)
	}
	if code := post(t, srv, pathSecret, headerSecret, `not json`); code != http.StatusOK {
		t.Fatalf("malformed: %d", code)
	}
	if code := post(t, srv, pathSecret, headerSecret, `{"timestamp":1}`); code != http.StatusOK {
		t.Fatalf("no update_type: %d", code)
	}
	if n := count(t, d, `SELECT count(*) FROM inbound_event`); n != 0 {
		t.Fatalf("invalid events stored: %d", n)
	}
}
