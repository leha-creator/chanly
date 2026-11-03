package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
)

type errorResponse struct {
	Error struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func newTestServer(t *testing.T, ready func(context.Context) error, authed bool) *httptest.Server {
	t.Helper()
	h, err := newHandler(serverDeps{
		Log:           logx.Discard(),
		Clock:         clock.System{},
		Metrics:       metrics.New(),
		Impl:          struct{ fallback }{},
		Ready:         ready,
		Authenticated: func(context.Context) bool { return authed },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, srv *httptest.Server, method, path, body string) (*http.Response, errorResponse) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var e errorResponse
	_ = json.Unmarshal(raw, &e)
	return resp, e
}

func TestServerErrors(t *testing.T) {
	srv := newTestServer(t, nil, true)
	cases := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"unknown route", http.MethodGet, "/api/v1/nope", "", http.StatusNotFound, "not_found"},
		{"not implemented", http.MethodGet, "/api/v1/cart", "", http.StatusNotImplemented, "not_implemented"},
		{"invalid body", http.MethodPost, "/api/v1/auth/code", `{"email":"not-an-email"}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"mock operations are excluded", http.MethodPost, "/api/v1/_mock/reset", "", http.StatusNotFound, "not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, e := doJSON(t, srv, c.method, c.path, c.body)
			if resp.StatusCode != c.status || e.Error.Code != c.code {
				t.Fatalf("got %d %q, want %d %q", resp.StatusCode, e.Error.Code, c.status, c.code)
			}
			rid := resp.Header.Get("X-Request-Id")
			if _, err := uuid.Parse(rid); err != nil || e.RequestID != rid {
				t.Fatalf("request id header %q, body %q", rid, e.RequestID)
			}
		})
	}
}

func TestServerValidationField(t *testing.T) {
	srv := newTestServer(t, nil, true)
	_, e := doJSON(t, srv, http.MethodPost, "/api/v1/auth/login", `{"email":"a@example.ru","code":"12"}`)
	if e.Error.Details["field"] != "code" {
		t.Fatalf("details = %v, want field=code", e.Error.Details)
	}
}

func TestServerRequiresSession(t *testing.T) {
	srv := newTestServer(t, nil, false)
	resp, e := doJSON(t, srv, http.MethodGet, "/api/v1/me", "")
	if resp.StatusCode != http.StatusUnauthorized || e.Error.Code != "unauthorized" {
		t.Fatalf("got %d %q", resp.StatusCode, e.Error.Code)
	}
	// Операция с security: [] доходит до обработчика без сессии.
	resp, e = doJSON(t, srv, http.MethodPost, "/api/v1/auth/code", `{"email":"a@example.ru"}`)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("public operation: got %d %q", resp.StatusCode, e.Error.Code)
	}
}

func TestServerKeepsValidRequestID(t *testing.T) {
	srv := newTestServer(t, nil, true)
	id := "0190a000-0000-7000-8000-00000000abcd"
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/livez", nil)
	req.Header.Set("X-Request-Id", id)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Request-Id") != id {
		t.Fatalf("X-Request-Id = %q, want %q", resp.Header.Get("X-Request-Id"), id)
	}
}

func TestServerHealth(t *testing.T) {
	down := newTestServer(t, func(context.Context) error { return errors.New("db is down") }, true)
	if resp, _ := doJSON(t, down, http.MethodGet, "/readyz", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz without db: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, down, http.MethodGet, "/livez", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("livez: %d", resp.StatusCode)
	}
	up := newTestServer(t, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("no deadline")
		}
		return nil
	}, true)
	if resp, _ := doJSON(t, up, http.MethodGet, "/readyz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz: %d", resp.StatusCode)
	}
}

func TestServerSecurityHeaders(t *testing.T) {
	srv := newTestServer(t, nil, true)
	resp, _ := doJSON(t, srv, http.MethodGet, "/api/v1/cart", "")
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

func TestMetricsServer(t *testing.T) {
	reg := metrics.New()
	reg.ObserveHTTP("getCart", "GET", 501, 10*time.Millisecond)
	s := metricsServer(":0", reg, nil, nil)
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `maxim_http_requests_total{code="501",method="GET",route="getCart"} 1`) {
		t.Fatalf("metric not exported:\n%s", rec.Body.String())
	}
}
