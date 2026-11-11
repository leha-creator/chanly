package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

type rwViewFixture struct {
	run  domain.Run
	next string
}

func (f *rwViewFixture) Run(context.Context, string) (domain.Run, error) { return f.run, nil }
func (f *rwViewFixture) Grant(context.Context, string) (domain.Grant, error) {
	return domain.Grant{Policy: domain.Policy{ArmingID: "outside-db"}}, nil
}
func (f *rwViewFixture) Status(context.Context, string) (map[string]any, error) {
	return map[string]any{"next_step": f.next}, nil
}

func TestRWCheckHTTPRejectsUnscopedRequests(t *testing.T) {
	h := &rwCheckHTTP{runID: "approved-run", token: strings.Repeat("a", 32)}
	for _, test := range []struct {
		method, path, token, body, match string
		status                           int
	}{
		{"GET", "/check", "", "", "", 401},
		{"GET", "/check?run=other", h.token, "", "", 404},
		{"POST", "/check", h.token, `{"amount":1}`, h.runID, 400},
		{"POST", "/check", h.token, "", "other-run", 412},
		{"POST", "/check", h.token, "", "", 412},
		{"DELETE", "/check", h.token, "", "", 405},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+test.token)
		r.Header.Set("If-Match", test.match)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s %s: %d", test.method, test.path, w.Code)
		}
	}
}

func TestRWCheckHTTPConcurrentLostResponseStartsSameRunOnce(t *testing.T) {
	manifest, _ := json.Marshal(domain.Manifest{Executor: domain.Executor{INN: "123456789012"}})
	f := &rwViewFixture{run: domain.Run{ID: "approved-run", State: "prepared", Manifest: manifest, Nominal: 1000, FeeCeiling: 100}, next: "create"}
	calls := 0
	h := &rwCheckHTTP{store: f, runID: f.run.ID, token: strings.Repeat("a", 32), armingID: "outside-db", log: logx.Discard()}
	h.advance = func(context.Context, string, bool) error {
		calls++
		now := time.Now()
		f.run.ReservedAt = &now
		f.next = "reconcile"
		return errors.New("lost response")
	}
	request := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/check", nil)
		r.Header.Set("Authorization", "Bearer "+h.token)
		r.Header.Set("If-Match", h.runID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet); w.Code != 200 || calls != 0 {
		t.Fatal("GET caused execution")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if w := request(http.MethodPost); w.Code != 200 {
				t.Errorf("start: %d", w.Code)
			}
		})
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("duplicate start calls: %d", calls)
	}
	// A new process sees the durable reservation and cannot restart the run.
	restarted := &rwCheckHTTP{store: f, runID: h.runID, token: h.token, armingID: h.armingID, log: h.log, advance: h.advance}
	r := httptest.NewRequest(http.MethodPost, "/check", nil)
	r.Header.Set("Authorization", "Bearer "+h.token)
	r.Header.Set("If-Match", h.runID)
	w := httptest.NewRecorder()
	restarted.ServeHTTP(w, r)
	if calls != 1 || !strings.Contains(w.Body.String(), "reconcile_required") || strings.Contains(w.Body.String(), "123456789012") {
		t.Fatalf("unsafe restart or PII: %d %s", calls, w.Body.String())
	}
}

func TestRWCheckGatewayConfigIsolation(t *testing.T) {
	for _, test := range []struct {
		env, url, token string
		valid           bool
	}{
		{EnvDevelopment, "http://rocketwork-check-ui:8082", strings.Repeat("a", 32), true},
		{EnvProduction, "https://trusted.example", strings.Repeat("a", 32), false},
		{EnvDevelopment, "http://arbitrary.example", strings.Repeat("a", 32), false},
		{EnvDevelopment, "https:", strings.Repeat("a", 32), false},
		{EnvDevelopment, "https://trusted.example", "short", false},
	} {
		_, err := LoadConfig(map[string]string{"APP_ENV": test.env, "DATABASE_URL": "postgres://x@y/z", "ROCKETWORK_CHECK_GATEWAY_URL": test.url, "ROCKETWORK_CHECK_GATEWAY_TOKEN": test.token}, ModeMigrate)
		if (err == nil) != test.valid {
			t.Fatalf("gateway %s %s: %v", test.env, test.url, err)
		}
	}
}
