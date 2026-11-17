package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/apigen"
	nd "github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/notificationtest"
)

func telegramTestConfig() Config {
	cfg := testConfig()
	cfg.Telegram = TelegramConfig{Enabled: true, BotName: "@LordChanlyBot", BotToken: "123:test", WebhookSecret: strings.Repeat("s", 32), WebhookURL: "https://dev.example.test/api/v1/webhooks/telegram"}
	return cfg
}

func TestTelegramConfigRequiresCompleteSecrets(t *testing.T) {
	e := validAPIEnv()
	e["TELEGRAM_ENABLED"] = "true"
	if _, err := LoadConfig(e, ModeAPI); err == nil {
		t.Fatal("incomplete Telegram accepted")
	}
	e["TELEGRAM_BOT_NAME"] = "@LordChanlyBot"
	e["TELEGRAM_BOT_TOKEN"] = "123:test"
	e["TELEGRAM_WEBHOOK_SECRET"] = strings.Repeat("s", 32)
	e["TELEGRAM_WEBHOOK_URL"] = "https://dev.example.test/api/v1/webhooks/telegram"
	if _, err := LoadConfig(e, ModeAPI); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramWebhookRejectsForgedAndGroupUpdates(t *testing.T) {
	cfg := telegramTestConfig()
	h := newTelegramWebhook(cfg, &infra{}, nil, nil)
	for _, tc := range []struct {
		secret, body string
		status       int
	}{
		{"wrong", `{}`, 401},
		{cfg.Telegram.WebhookSecret, `{}`, 400},
		{cfg.Telegram.WebhookSecret, `{"update_id":1,"message":{"text":"/start notify_` + strings.Repeat("x", 43) + `","from":{"id":41},"chat":{"id":-1,"type":"group"}}}`, 200},
	} {
		r := httptest.NewRequest("POST", "/api/v1/webhooks/telegram", strings.NewReader(tc.body))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", tc.secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d", w.Code, tc.status)
		}
	}
}

func createTelegramBot(t *testing.T, c *client) apigen.TelegramBotConnectionCreated {
	t.Helper()
	r := c.do("POST", "/me/telegram-bot/connections", nil)
	if r.status != 201 {
		t.Fatalf("create Telegram: %d %s", r.status, r.body)
	}
	var v apigen.TelegramBotConnectionCreated
	r.json(t, &v)
	return v
}

func telegramConnected(t *testing.T, c *client) bool {
	t.Helper()
	r := c.do("GET", "/me/telegram-bot", nil)
	if r.status != 200 {
		t.Fatalf("status: %d %s", r.status, r.body)
	}
	var v apigen.TelegramBot
	r.json(t, &v)
	return v.Connected
}

func telegramStart(t *testing.T, a *testApp, link string, user, id int64) {
	t.Helper()
	u, _ := url.Parse(link)
	raw := fmt.Sprintf(`{"update_id":%d,"message":{"text":%q,"from":{"id":%d},"chat":{"id":%d,"type":"private"}}}`, id, "/start "+u.Query().Get("start"), user, user)
	req, _ := http.NewRequest("POST", a.srv.URL+BaseURL+"/webhooks/telegram", strings.NewReader(raw))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", a.mods.cfg.Telegram.WebhookSecret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	a.settle()
}

func TestTelegramPersonalBindingLifecycle(t *testing.T) {
	a := newTestAppWith(t, telegramTestConfig())
	c := a.newAdvertiser("telegram-personal@example.test")
	other := a.newAdvertiser("telegram-other@example.test")
	delivery := &notificationtest.Delivery{}
	a.mods.notification.Telegram = delivery
	if telegramConnected(t, c) {
		t.Fatal("connected without proof")
	}
	v := createTelegramBot(t, c)
	if !strings.HasPrefix(v.BotUrl, "https://t.me/LordChanlyBot?start=notify_") {
		t.Fatal("incorrect bot link")
	}
	path := "/me/telegram-bot/connections/" + v.Id.String()
	if r := other.do("GET", path, nil); r.status != 404 {
		t.Fatal("foreign connection visible")
	}
	c.csrf = false
	if r := c.do("POST", "/me/telegram-bot/connections", nil); r.status != 403 {
		t.Fatal("create without CSRF")
	}
	c.csrf = true
	telegramStart(t, a, v.BotUrl, 8101, 1)
	if !telegramConnected(t, c) || telegramConnected(t, other) {
		t.Fatal("wrong binding owner")
	}
	u, _ := url.Parse(v.BotUrl)
	token := strings.TrimPrefix(u.Query().Get("start"), "notify_")
	var stored string
	if err := a.db.Pool.QueryRow(t.Context(), "SELECT payload::text FROM inbound_event WHERE source='telegram'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, token) {
		t.Fatal("raw token persisted")
	}
	var proof map[string]any
	if json.Unmarshal([]byte(stored), &proof) != nil || proof["token_hash"] == nil {
		t.Fatal("missing proof")
	}
	for _, o := range accountSettings(t, c).Catalog {
		if o.Channel == "telegram_bot" && !o.Available {
			t.Fatal("Telegram unavailable after binding")
		}
	}
	if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleAdvertiser, OrganizationID: c.orgID(t), Template: "balance.deposited", Data: map[string]any{"amount": 10000}}); err != nil {
		t.Fatal(err)
	}
	a.settle()
	if len(delivery.Messages()) != 1 {
		t.Fatalf("Telegram deliveries %d", len(delivery.Messages()))
	}
	// Queue another event and disconnect before its delivery attempt.
	if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleAdvertiser, OrganizationID: c.orgID(t), Template: "balance.deposited", Data: map[string]any{"amount": 20000}}); err != nil {
		t.Fatal(err)
	}
	if r := c.do("DELETE", "/me/telegram-bot", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	a.settle()
	if len(delivery.Messages()) != 1 {
		t.Fatal("queued notification delivered after disconnect")
	}
	var suppressed int
	if err := a.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM notification WHERE channel='telegram_bot' AND status='suppressed'").Scan(&suppressed); err != nil || suppressed != 1 {
		t.Fatalf("suppressed=%d error=%v", suppressed, err)
	}
	telegramStart(t, a, v.BotUrl, 8102, 2)
	if telegramConnected(t, c) {
		t.Fatal("replay reconnected")
	}
	// A newer link revokes the previous challenge, and expiration cannot bind.
	old := createTelegramBot(t, c)
	latest := createTelegramBot(t, c)
	telegramStart(t, a, old.BotUrl, 8101, 3)
	if telegramConnected(t, c) {
		t.Fatal("superseded link accepted")
	}
	a.clk.Advance(10 * time.Minute)
	telegramStart(t, a, latest.BotUrl, 8101, 4)
	if telegramConnected(t, c) {
		t.Fatal("expired link accepted")
	}
}
