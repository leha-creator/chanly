package max_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

const token = "bot-secret-token"

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newClient(t *testing.T, srv *httptest.Server, httpClient *http.Client) (*max.Client, *dbtest.DB) {
	t.Helper()
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	log := logx.Discard()
	noSleep := func(context.Context, time.Duration) error { return nil }
	out := outbound.New(d.Pool, clk, log, outbound.Options{System: outbound.SystemMAX, HTTP: httpClient, Sleep: noSleep})
	lim := ratelimit.New(d.Pool, clk, log, ratelimit.Options{Sleep: func(_ context.Context, w time.Duration) error {
		clk.Advance(w)
		return nil
	}})
	return max.New(out, lim, max.Options{BaseURL: srv.URL, Token: token, Sleep: noSleep}, log), d
}

func TestSendChatMessage(t *testing.T) {
	var got struct {
		auth, query string
		body        map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.query = r.Header.Get("Authorization"), r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		_, _ = w.Write(testdata(t, "message_sent.json"))
	}))
	defer srv.Close()
	c, d := newClient(t, srv, nil)
	id, err := c.SendChatMessage(context.Background(), -100500, "Alert")
	if err != nil || id != "mid.0001" {
		t.Fatalf("id %q err %v", id, err)
	}
	if got.auth != token || got.query != "chat_id=-100500" || got.body["text"] != "Alert" || got.body["format"] != "markdown" {
		t.Fatalf("request %+v", got)
	}
	var stored string
	if err := d.Pool.QueryRow(context.Background(), `SELECT request::text FROM outbound_request`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, token) {
		t.Fatal("bot token stored in outbound_request")
	}
}

func TestGetMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(testdata(t, "me.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	bot, err := c.GetMe(context.Background())
	if err != nil || bot.UserID != 777 || bot.Username != "maxim_bot" {
		t.Fatalf("bot %+v err %v", bot, err)
	}
}

func TestSuccessFalseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"chat.not.found"}`))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.SendChatMessage(context.Background(), 1, "x"); !job.IsPermanent(err) || !strings.Contains(err.Error(), "chat.not.found") {
		t.Fatalf("err %v", err)
	}
}

func TestUnauthorizedIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(testdata(t, "error_unauthorized.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	_, err := c.GetMe(context.Background())
	apiErr, ok := max.AsAPIError(err)
	if !job.IsPermanent(err) || !ok || apiErr.Code != "verify.token" {
		t.Fatalf("err %v api %+v", err, apiErr)
	}
}

func TestTooManyRequestsRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(testdata(t, "message_sent.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.SendUserMessage(context.Background(), 5, "hi"); err != nil || n.Load() != 2 {
		t.Fatalf("err %v calls %d", err, n.Load())
	}
}

func TestTextTooLongRejectedWithoutRequest(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1) }))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.SendChatMessage(context.Background(), 1, strings.Repeat("я", max.MaxTextLength+1)); !job.IsPermanent(err) || n.Load() != 0 {
		t.Fatalf("err %v calls %d", err, n.Load())
	}
}

func TestCACertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testdata(t, "me.json"))
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	trusted, err := max.NewHTTPClient(caFile)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newClient(t, srv, trusted)
	if _, err := c.GetMe(context.Background()); err != nil {
		t.Fatalf("with CA: %v", err)
	}
	untrusted, _ := max.NewHTTPClient("")
	c, _ = newClient(t, srv, untrusted)
	if _, err := c.GetMe(context.Background()); err == nil {
		t.Fatal("server certificate trusted without CA file")
	}
	if _, err := max.NewHTTPClient(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing CA file accepted")
	}
}

// Личное сообщение пользователю без диалога: 403 — отказ без повторов (Q-38, E-1).
func TestSendUserMessageDeniedIsPermanent(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"chat.denied","message":"chat.denied"}`))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	_, err := c.SendUserMessage(context.Background(), 5, "hi")
	if !job.IsPermanent(err) || n.Load() != 1 {
		t.Fatalf("err %v calls %d", err, n.Load())
	}
}
