package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
)

func checkTestEnv() map[string]string {
	return map[string]string{
		"APP_ENV": "development", "DATABASE_URL": "postgres://localhost/checks",
		"MAX_BOT_TOKEN": "private-test-token", "INTEGRATION_CHECK_MAX_CHAT_ID": "-100",
	}
}

func TestIntegrationCheckPreflight(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]string
		args []string
		code int
	}{
		{"configuration only", nil, []string{"--system", "max"}, 0},
		{"test forces fake", map[string]string{"APP_ENV": "test"}, []string{"--system", "max"}, 1},
		{"production forbidden", map[string]string{"APP_ENV": "production"}, []string{"--system", "max"}, 1},
		{"missing token", map[string]string{"MAX_BOT_TOKEN": ""}, []string{"--system", "max"}, 1},
		{"missing channel", map[string]string{"INTEGRATION_CHECK_MAX_CHAT_ID": ""}, []string{"--system", "max"}, 1},
		{"different channel", nil, []string{"--system", "max", "--chat", "-101"}, 1},
		{"insecure endpoint", map[string]string{"MAX_API_URL": "http://platform-api2.max.ru"}, []string{"--system", "max"}, 1},
		{"endpoint userinfo", map[string]string{"MAX_API_URL": "https://private-test-token@platform-api2.max.ru"}, []string{"--system", "max"}, 1},
		{"endpoint query", map[string]string{"MAX_API_URL": max.DefaultBaseURL + "?token=private-test-token"}, []string{"--system", "max"}, 1},
		{"malformed number", map[string]string{"MAX_RATE_GLOBAL": "private-test-token"}, []string{"--system", "max"}, 1},
		{"no implicit inspect", nil, []string{"--system", "max", "--scenario", "inspect"}, 2},
		{"unknown scenario", nil, []string{"--system", "max", "--scenario", "private-test-token"}, 2},
		{"publish needs state", nil, []string{"--system", "max", "--scenario", "publish", "--execute"}, 2},
		{"ord fake", nil, []string{"--system", "ord"}, 1},
		{"ord production url", map[string]string{"ORD_OAUTH_TOKEN": "private-test-token", "ORD_API_URL": "https://ord.yandex.net"}, []string{"--system", "ord"}, 1},
		{"rw missing manifest", nil, []string{"--system", "rocketwork"}, 2},
		{"rw unverified live", map[string]string{"ROCKETWORK_MODE": "live", "ROCKETWORK_TOKEN": "private-test-token"}, []string{"--system", "rocketwork"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := checkTestEnv()
			for k, v := range tc.set {
				vars[k] = v
			}
			var out bytes.Buffer
			code := Run(context.Background(), RunParams{Command: "integration-check", Args: tc.args, Environ: environ(vars), Stdout: &out, Stderr: &out})
			if code != tc.code {
				t.Fatalf("code %d want %d: %s", code, tc.code, out.String())
			}
			if strings.Contains(out.String(), "private-test-token") {
				t.Fatal("secret leaked")
			}
			if code == 0 && !strings.Contains(out.String(), `"live_verified":false`) {
				t.Fatal("preflight falsely implies live evidence")
			}
		})
	}
}

func TestIntegrationCheckORDPreflight(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
		code int
	}{
		{"sandbox configuration", nil, 0},
		{"test forces fake", map[string]string{"APP_ENV": "test"}, 1},
		{"production forbidden", map[string]string{"APP_ENV": "production"}, 1},
		{"disabled", map[string]string{"ORD_ENABLED": "false"}, 1},
		{"fake", map[string]string{"ORD_OAUTH_TOKEN": ""}, 1},
		{"missing KKTU", map[string]string{"ORD_KKTU_CODE": ""}, 1},
		{"missing platform", map[string]string{"ORD_PLATFORM_NAME": ""}, 1},
		{"automatic reports", map[string]string{"ORD_REPORT_AUTO_SEND": "true"}, 1},
		{"production endpoint", map[string]string{"ORD_API_URL": "https://ord.yandex.net"}, 1},
		{"insecure endpoint", map[string]string{"ORD_API_URL": "http://ord-prestable.yandex.net"}, 1},
		{"URL credentials", map[string]string{"ORD_API_URL": "https://private-ord-token@ord-prestable.yandex.net"}, 1},
		{"URL query", map[string]string{"ORD_API_URL": "https://ord-prestable.yandex.net?token=private-ord-token"}, 1},
		{"URL fragment", map[string]string{"ORD_API_URL": "https://ord-prestable.yandex.net#private-ord-token"}, 1},
		{"URL path", map[string]string{"ORD_API_URL": "https://ord-prestable.yandex.net/api"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := checkTestEnv()
			vars["ORD_OAUTH_TOKEN"] = "private-ord-token"
			vars["ORD_KKTU_CODE"] = "30.15.1"
			vars["ORD_PLATFORM_INN"] = devPlatformINN
			vars["ORD_PLATFORM_NAME"] = "private-platform-name"
			for k, v := range tc.set {
				vars[k] = v
			}
			var out bytes.Buffer
			code := Run(context.Background(), RunParams{Command: "integration-check", Args: []string{"--system", "ord"}, Environ: environ(vars), Stdout: &out, Stderr: &out})
			if code != tc.code {
				t.Fatalf("code %d want %d: %s", code, tc.code, out.String())
			}
			for _, secret := range []string{"private-ord-token", devPlatformINN, "private-platform-name"} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("ORD configuration leaked")
				}
			}
			if code == 0 && (!strings.Contains(out.String(), `"live_verified":false`) || !strings.Contains(out.String(), `"network_called":false`)) {
				t.Fatal("configuration check misrepresented as live evidence")
			}
		})
	}
}

func TestIntegrationCheckORDUnverifiedScenariosBlocked(t *testing.T) {
	for _, scenario := range []string{"publish", "observe", "recover", "remove"} {
		if _, err := parseIntegrationCheck([]string{"--system", "ord", "--scenario", scenario, "--execute", "--run-dir", t.TempDir()}); err == nil {
			t.Fatalf("unverified ORD scenario %s accepted", scenario)
		}
	}
}

func TestIntegrationCheckORDInspectConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    map[string]string
		wantOK bool
	}{
		{"read without contract configuration", nil, true},
		{"test fake", map[string]string{"APP_ENV": "test"}, false},
		{"disabled", map[string]string{"ORD_ENABLED": "false"}, false},
		{"fake", map[string]string{"ORD_OAUTH_TOKEN": ""}, false},
		{"production URL", map[string]string{"ORD_API_URL": "https://ord.yandex.net"}, false},
		{"automatic reports", map[string]string{"ORD_REPORT_AUTO_SEND": "true"}, false},
		{"path injection", map[string]string{"ORD_API_VERSION": "v8/../v7"}, false},
		{"rate too high", map[string]string{"ORD_RATE": "6"}, false},
		{"invalid rate", map[string]string{"ORD_RATE": "NaN"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := checkTestEnv()
			vars["ORD_OAUTH_TOKEN"] = "private-ord-token"
			for k, v := range tc.set {
				vars[k] = v
			}
			cfg, err := LoadConfig(vars, ModeDev)
			if err != nil {
				t.Fatal(err)
			}
			o, err := parseIntegrationCheck([]string{"--system", "ord", "--scenario", "inspect", "--execute"})
			if err != nil {
				t.Fatal(err)
			}
			if err := integrationCheckPreflight(cfg, vars, &o); (err == nil) != tc.wantOK {
				t.Fatalf("preflight error = %v", err)
			}
		})
	}
	if _, err := parseIntegrationCheck([]string{"--system", "ord", "--scenario", "inspect"}); err == nil {
		t.Fatal("implicit network call allowed")
	}
}

func TestIntegrationCheckORDInspectAdapter(t *testing.T) {
	d := dbtest.New(t)
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v8/organization" || !strings.HasPrefix(r.URL.Query().Get("object_id"), "access-check-") {
			t.Error("unexpected ORD request")
		}
		if r.Header.Get("Authorization") != "Bearer private-ord-token" {
			t.Error("missing authorization")
		}
		w.Header().Set("Location", "/redirect-target")
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, `{"detail":"Not found organization by id"}`)
	}))
	defer srv.Close()
	cfg, err := LoadConfig(checkTestEnv(), ModeDev)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ORD.APIURL, cfg.ORD.OAuthToken = srv.URL, "private-ord-token"
	var out bytes.Buffer
	in := &infra{pool: d.Pool, clock: clock.System{}, log: integrationCheckLogger(&out, slog.LevelDebug)}
	if err := in.inspectCheckORD(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(out.String(), `"authorization_verified":true`) || strings.Contains(out.String(), "private-ord-token") {
		t.Fatal("incorrect diagnostic result")
	}
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusFound} {
		status.Store(int32(code))
		before := calls.Load()
		out.Reset()
		if err := in.inspectCheckORD(context.Background(), cfg); err == nil {
			t.Fatal("failed request accepted")
		}
		if calls.Load() != before+1 {
			t.Fatal("diagnostic followed redirect or retried")
		}
		if strings.Contains(out.String(), `"authorization_verified":true`) {
			t.Fatal("failed request marked verified")
		}
	}
}

type checkMAXStub struct {
	maxAPI
	sends, deletes int
	message        *max.ChannelMessage
	sendErr        error
	findMissing    bool
	member         max.Membership
}

func (a *checkMAXStub) GetMe(context.Context) (max.Bot, error) { return max.Bot{UserID: 42}, nil }
func (a *checkMAXStub) GetChat(_ context.Context, id int64) (max.Chat, error) {
	return max.Chat{ChatID: id, Type: max.ChatTypeChannel}, nil
}
func (a *checkMAXStub) GetMembership(context.Context, int64) (max.Membership, error) {
	return a.member, nil
}
func (a *checkMAXStub) SendChannelPost(_ context.Context, _ int64, _ string, _ []string, _ max.Recovery) (max.PostedMessage, error) {
	a.sends++
	a.message = &max.ChannelMessage{MID: "test-message", SenderID: 42, ContentHash: "hash"}
	return max.PostedMessage{ID: "test-message", ContentHash: "hash"}, a.sendErr
}
func (a *checkMAXStub) GetMessage(context.Context, string) (max.ChannelMessage, error) {
	if a.message == nil {
		return max.ChannelMessage{}, max.ErrMessageNotFound
	}
	return *a.message, nil
}
func (a *checkMAXStub) FindPost(_ context.Context, _ int64, _, _ time.Time, sender int64, _ string) (*max.ChannelMessage, error) {
	if a.findMissing || (a.message != nil && a.message.SenderID != sender) {
		return nil, nil
	}
	return a.message, nil
}
func (a *checkMAXStub) DeleteMessage(context.Context, int64, string) error {
	a.deletes++
	a.message = nil
	return nil
}

func checkRunner(t *testing.T, a *checkMAXStub) (func(string) error, *clock.Manual, string) {
	t.Helper()
	dir := t.TempDir()
	clk := clock.NewManual(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	run := func(scenario string) error {
		return executeMAXCheck(context.Background(), a, clk, log, integrationCheckOptions{
			system: "max", scenario: scenario, chatID: -100, runDir: dir, execute: true,
		})
	}
	return run, clk, dir
}

func TestIntegrationCheckMAXLifecycle(t *testing.T) {
	a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: []string{"write", "delete"}}}
	run, clk, _ := checkRunner(t, a)
	if err := run("publish"); err != nil {
		t.Fatal(err)
	}
	if err := run("publish"); err == nil {
		t.Fatal("duplicate publication allowed")
	}
	if err := run("observe"); err != nil {
		t.Fatal(err)
	}
	if err := run("remove"); err == nil || a.deletes != 0 {
		t.Fatal("early deletion allowed")
	}
	clk.Advance(72*time.Hour - time.Second)
	if err := run("remove"); err == nil {
		t.Fatal("72h boundary not enforced")
	}
	clk.Advance(time.Second)
	if err := run("remove"); err != nil {
		t.Fatal(err)
	}
	if err := run("remove"); err != nil {
		t.Fatal(err)
	}
	if a.sends != 1 || a.deletes != 1 {
		t.Fatalf("sends=%d deletes=%d", a.sends, a.deletes)
	}
}

func TestIntegrationCheckMutationPermissions(t *testing.T) {
	for _, permissions := range [][]string{nil, {"write"}, {"delete"}} {
		a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: permissions}}
		run, _, dir := checkRunner(t, a)
		if err := run("publish"); err == nil || a.sends != 0 {
			t.Fatal("publication without write and cleanup rights allowed")
		}
		if _, err := os.Stat(filepath.Join(dir, "state.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("blocked publication created state")
		}
	}
	a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: []string{"write", "delete"}}}
	run, clk, _ := checkRunner(t, a)
	if err := run("publish"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(72 * time.Hour)
	a.member.Permissions = []string{"write"}
	if err := run("remove"); err == nil || a.deletes != 0 {
		t.Fatal("removal after permission revocation allowed")
	}
	if err := run("observe"); err != nil {
		t.Fatalf("read-only observation blocked: %v", err)
	}
}

func TestIntegrationCheckSenderlessPost(t *testing.T) {
	a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: []string{"write", "delete"}}}
	run, clk, _ := checkRunner(t, a)
	if err := run("publish"); err != nil {
		t.Fatal(err)
	}
	a.message.SenderID = 0
	if err := run("observe"); err != nil {
		t.Fatal(err)
	}
	a.findMissing = true
	if err := run("observe"); err == nil {
		t.Fatal("senderless post without run marker accepted")
	}
	clk.Advance(72 * time.Hour)
	if err := run("remove"); err == nil || a.deletes != 0 {
		t.Fatal("senderless post without run marker deleted")
	}
	a.findMissing = false
	if err := run("remove"); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationCheckUnknownPublication(t *testing.T) {
	a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: []string{"write", "delete"}}, sendErr: errors.New("connection lost after acceptance")}
	run, clk, _ := checkRunner(t, a)
	if err := run("publish"); err == nil {
		t.Fatal("uncertain send passed")
	}
	if err := run("publish"); err == nil {
		t.Fatal("uncertain send repeated")
	}
	a.findMissing = true
	if err := run("recover"); err == nil {
		t.Fatal("missing post falsely recovered")
	}
	a.findMissing = false
	clk.Advance(time.Hour)
	if err := run("recover"); err != nil {
		t.Fatal(err)
	}
	if err := run("observe"); err != nil {
		t.Fatal(err)
	}
	if a.sends != 1 {
		t.Fatal("recovery posted again")
	}
}

func TestIntegrationCheckOwnershipAndAbsence(t *testing.T) {
	for _, scenario := range []string{"wrong sender", "different channel", "already absent", "lost marker", "hash differs", "no admin"} {
		t.Run(scenario, func(t *testing.T) {
			a := &checkMAXStub{member: max.Membership{UserID: 42, IsAdmin: true, Permissions: []string{"write", "delete"}}}
			run, clk, dir := checkRunner(t, a)
			if err := run("publish"); err != nil {
				t.Fatal(err)
			}
			clk.Advance(73 * time.Hour)
			action := "remove"
			switch scenario {
			case "wrong sender":
				a.message.SenderID = 99
			case "different channel":
				s, unlock, err := openMAXCheckRun(dir, false, 42, -100, clk.Now())
				if err != nil {
					t.Fatal(err)
				}
				s.ChatID = -101
				if err := saveMAXCheckRun(dir, s); err != nil {
					t.Fatal(err)
				}
				unlock()
			case "already absent":
				a.message = nil
			case "lost marker":
				a.findMissing = true
			case "hash differs":
				a.message.ContentHash = "other"
				action = "observe"
			case "no admin":
				a.member.IsAdmin = false
			}
			if err := run(action); err == nil {
				t.Fatal("unsafe/misleading result accepted")
			}
			if a.deletes != 0 {
				t.Fatal("foreign or absent post deleted")
			}
		})
	}
}

func TestIntegrationCheckRunLockAndCorruption(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	_, unlock, err := openMAXCheckRun(dir, true, 42, -100, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := openMAXCheckRun(dir, true, 42, -100, now); err == nil {
		t.Fatal("concurrent run allowed")
	}
	unlock()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openMAXCheckRun(dir, true, 42, -100, now); err == nil {
		t.Fatal("corrupt state overwritten")
	}
}

// Exercise the actual adapter, database-backed outbound and persistence together.
// The HTTP server models acceptance followed by 503: no second POST is allowed.
func TestIntegrationCheckMAXAdapterUncertainSend(t *testing.T) {
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	var posts atomic.Int32
	var postText string
	var mu sync.Mutex
	message := func() map[string]any {
		return map[string]any{"timestamp": clk.Now().UnixMilli(),
			"sender": map[string]any{"user_id": 42, "is_bot": true},
			"body":   map[string]any{"mid": "check-post", "text": postText},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me":
			_, _ = io.WriteString(w, `{"user_id":42}`)
		case "/chats/-100":
			_, _ = io.WriteString(w, `{"chat_id":-100,"type":"channel"}`)
		case "/chats/-100/members/me":
			_, _ = io.WriteString(w, `{"user_id":42,"is_admin":true,"permissions":["write","delete"]}`)
		case "/messages":
			if r.Method == http.MethodPost {
				posts.Add(1)
				var body struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				postText = body.Text
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"code":"service.unavailable"}`)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{message()}})
			}
		case "/messages/check-post":
			_ = json.NewEncoder(w).Encode(message())
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cfg, err := LoadConfig(checkTestEnv(), ModeDev)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MAX.APIURL = srv.URL // test-only wiring; public CLI rejects non-approved endpoints
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	in := &infra{pool: d.Pool, clock: clk, log: log}
	if err := in.connectCheckMAX(cfg); err != nil {
		t.Fatal(err)
	}
	o := integrationCheckOptions{system: "max", scenario: "publish", chatID: -100, runDir: t.TempDir(), execute: true}
	if err := executeMAXCheck(context.Background(), in.max, clk, log, o); err == nil {
		t.Fatal("ambiguous result accepted")
	}
	if posts.Load() != 1 {
		t.Fatalf("unsafe inline retry: %d POSTs", posts.Load())
	}
	if err := executeMAXCheck(context.Background(), in.max, clk, log, o); err == nil {
		t.Fatal("publication repeated")
	}
	o.scenario = "recover"
	if err := executeMAXCheck(context.Background(), in.max, clk, log, o); err != nil {
		t.Fatal(err)
	}
	o.scenario = "observe"
	if err := executeMAXCheck(context.Background(), in.max, clk, log, o); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("recovery sent another POST")
	}
}

func TestIntegrationCheckProviderLogsOmitBodies(t *testing.T) {
	var out bytes.Buffer
	log := integrationCheckLogger(&out, slog.LevelDebug)
	log.With("response", "unredacted-personal-data").Error("provider failure", "error", "token-echo", "url", "secret-url", "status", 503)
	for _, secret := range []string{"unredacted-personal-data", "token-echo", "secret-url"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("provider data leaked into diagnostic logs")
		}
	}
	if !strings.Contains(out.String(), `"status":503`) {
		t.Fatal("status missing")
	}
}
