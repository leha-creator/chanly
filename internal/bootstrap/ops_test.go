package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/metrics"
)

func environ(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func TestProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ok.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	cases := []struct {
		name string
		args []string
		code int
	}{
		{"200", []string{ok.URL + "/readyz"}, 0},
		{"503", []string{ok.URL + "/other"}, 1},
		{"connection refused", []string{closed.URL + "/readyz"}, 1},
		{"no url", nil, 2},
		{"two urls", []string{ok.URL, ok.URL}, 2},
		{"not http", []string{"ftp://example.ru/readyz"}, 2},
		{"no host", []string{"http:///readyz"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), RunParams{Command: ModeProbe, Args: c.args, Stdout: &stdout, Stderr: &stderr})
			if code != c.code {
				t.Fatalf("code = %d, want %d (stderr %q)", code, c.code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("probe must not log, got %q", stdout.String())
			}
			if c.code != 0 && stderr.Len() == 0 {
				t.Fatal("probe failure must explain itself in stderr")
			}
		})
	}
}

func TestConfigCheck(t *testing.T) {
	worker := validAPIEnv()
	worker["SMTP_HOST"] = "localhost"
	worker["SMTP_PORT"] = "1025"
	worker["SMTP_TLS"] = "none"
	worker["MAIL_FROM"] = "noreply@maxim.local"
	prod := validAPIEnv()
	prod["APP_ENV"] = "production"

	cases := []struct {
		name   string
		args   []string
		env    map[string]string
		code   int
		expect string
	}{
		{"api valid", []string{"check", "--for", "api"}, validAPIEnv(), 0, `"msg":"config valid"`},
		{"migrate valid", []string{"check", "--for", "migrate"}, validAPIEnv(), 0, `"mode":"migrate"`},
		{"worker valid", []string{"check", "--for", "worker"}, worker, 0, `"mode":"worker"`},
		{"worker without smtp", []string{"check", "--for", "worker"}, validAPIEnv(), 1, "SMTP_HOST: required"},
		{"production api without secrets", []string{"check", "--for", "api"}, prod, 1, "COOKIE_SECURE: must be true in production"},
		{"unknown mode", []string{"check", "--for", "probe"}, validAPIEnv(), 2, "invalid arguments"},
		{"no subcommand", nil, validAPIEnv(), 2, "invalid arguments"},
		{"extra argument", []string{"check", "--for", "api", "x"}, validAPIEnv(), 2, "invalid arguments"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), RunParams{Command: ModeConfig, Args: c.args, Environ: environ(c.env),
				Version: "test", Stdout: &stdout, Stderr: &stderr})
			if code != c.code {
				t.Fatalf("code = %d, want %d\n%s", code, c.code, stdout.String())
			}
			if !strings.Contains(stdout.String(), c.expect) {
				t.Fatalf("output %q does not contain %q", stdout.String(), c.expect)
			}
		})
	}
}

func TestReadyzHandler(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	okCheck := func(context.Context) error { return nil }
	var life lifecycle
	cases := []struct {
		name   string
		ready  func(context.Context) error
		stop   bool
		status int
		body   string
	}{
		{"ready", life.ready(okCheck), false, http.StatusOK, "ok"},
		{"database down", life.ready(func(context.Context) error { return errors.New("db down") }), false, http.StatusServiceUnavailable, "not ready"},
		{"no check", nil, false, http.StatusServiceUnavailable, "not ready"},
		{"stopping", life.ready(okCheck), true, http.StatusServiceUnavailable, "draining"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			life.stopping.Store(c.stop)
			rec := httptest.NewRecorder()
			readyzHandler(log, c.ready)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != c.status || body["status"] != c.body {
				t.Fatalf("got %d %v, want %d %q", rec.Code, body, c.status, c.body)
			}
		})
	}
}

func TestMetricsServerHealth(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var life lifecycle
	s := metricsServer(":0", metrics.New(), log, life.ready(func(context.Context) error { return nil }))
	for _, path := range []string{"/livez", "/readyz"} {
		rec := httptest.NewRecorder()
		s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	life.stopping.Store(true)
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while stopping: %d", rec.Code)
	}
	// Без проверки готовности (api) — только /metrics.
	plain := metricsServer(":0", metrics.New(), nil, nil)
	rec = httptest.NewRecorder()
	plain.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("api metrics port must not serve readyz: %d", rec.Code)
	}
}

// TestServeDrain: после отмены контекста /readyz отвечает 503, сервер ещё принимает
// запросы в течение паузы и закрывается после неё.
func TestServeDrain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	var life lifecycle
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", readyzHandler(log, life.ready(func(context.Context) error { return nil })))
	mux.HandleFunc("GET /livez", livezHandler)

	const drain = 400 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, log, stopPolicy{life: &life, drain: drain}, namedServer{name: "api", srv: newHTTPServer(addr, mux)})
	}()
	get := func(path string) (int, error) {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if code, err := get("/readyz"); err == nil && code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	cancel()
	time.Sleep(50 * time.Millisecond)
	if code, err := get("/readyz"); err != nil || code != http.StatusServiceUnavailable {
		t.Fatalf("readyz during drain: %d %v", code, err)
	}
	if code, err := get("/livez"); err != nil || code != http.StatusOK {
		t.Fatalf("requests must be served during drain: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if elapsed := time.Since(start); elapsed < drain {
		t.Fatalf("server stopped after %v, before drain %v", elapsed, drain)
	}
	if _, err := get("/livez"); err == nil {
		t.Fatal("server still accepts connections after shutdown")
	}
	if !slices.ContainsFunc(strings.Split(logs.String(), "\n"), func(l string) bool { return strings.Contains(l, `"msg":"drain started"`) }) {
		t.Fatalf("no drain log:\n%s", logs.String())
	}
}

func TestShutdownDrainBounds(t *testing.T) {
	for _, v := range []string{"0s", "5s", "60s"} {
		e := validAPIEnv()
		e["HTTP_SHUTDOWN_DRAIN"] = v
		cfg, err := LoadConfig(e, ModeAPI)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if want, _ := time.ParseDuration(v); cfg.ShutdownDrain != want {
			t.Fatalf("ShutdownDrain = %v, want %v", cfg.ShutdownDrain, want)
		}
	}
	for _, v := range []string{"-1s", "61s"} {
		e := validAPIEnv()
		e["HTTP_SHUTDOWN_DRAIN"] = v
		_, err := LoadConfig(e, ModeAPI)
		if !slices.Contains(configFields(t, err), "HTTP_SHUTDOWN_DRAIN: must be between 0s and 60s") {
			t.Fatalf("%s: fields %v", v, configFields(t, err))
		}
	}
}
