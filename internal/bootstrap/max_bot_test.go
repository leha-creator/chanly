package bootstrap

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	nd "github.com/leha-creator/chanly/internal/notification/domain"
)

func createMAXBot(t *testing.T, c *client) apigen.MaxBotConnectionCreated {
	t.Helper()
	r := c.do("POST", "/me/max-bot/connections", nil)
	if r.status != 201 {
		t.Fatalf("create MAX %d %s", r.status, r.body)
	}
	var v apigen.MaxBotConnectionCreated
	r.json(t, &v)
	return v
}
func bindMAXBot(t *testing.T, a *testApp, c *client, id int64) apigen.MaxBotConnectionCreated {
	t.Helper()
	v := createMAXBot(t, c)
	u, err := url.Parse(v.BotUrl)
	if err != nil {
		t.Fatal(err)
	}
	a.startConnection(u.Query().Get("start"), id)
	return v
}
func maxConnected(t *testing.T, c *client) bool {
	t.Helper()
	r := c.do("GET", "/me/max-bot", nil)
	if r.status != 200 {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	var v apigen.MaxBot
	r.json(t, &v)
	return v.Connected
}
func TestMAXBotPersonalBindingLifecycle(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("max-personal@example.test")
	other := a.newAdvertiser("max-other@example.test")
	if maxConnected(t, c) {
		t.Fatal("connected without proof")
	}
	v := createMAXBot(t, c)
	path := "/me/max-bot/connections/" + v.Id.String()
	if r := other.do("GET", path, nil); r.status != 404 {
		t.Fatal("foreign session visible", r.status)
	}
	c.csrf = false
	if r := c.do("POST", "/me/max-bot/connections", nil); r.status != 403 {
		t.Fatal("missing CSRF accepted", r.status)
	}
	c.csrf = true
	u, _ := url.Parse(v.BotUrl)
	payload := u.Query().Get("start")
	token := strings.TrimPrefix(payload, "notify_")
	a.startConnection(payload, 8101)
	if !maxConnected(t, c) || maxConnected(t, other) {
		t.Fatal("incorrect binding owner")
	}
	if r := c.do("GET", path, nil); r.status != 200 || !strings.Contains(string(r.body), "connected") {
		t.Fatalf("connection %d %s", r.status, r.body)
	}
	var stored string
	if err := a.db.Pool.QueryRow(t.Context(), "SELECT payload::text FROM inbound_event WHERE event_type='bot_started' LIMIT 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, token) {
		t.Fatal("raw token persisted")
	}
	for _, o := range accountSettings(t, c).Catalog {
		if o.Channel == "max_bot" && !o.Available {
			t.Fatal("advertiser MAX unavailable after binding")
		}
	}
	before := len(a.maxFake().Sent())
	if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleAdvertiser, OrganizationID: c.orgID(t), Template: "balance.deposited", Data: map[string]any{"amount": 10000}}); err != nil {
		t.Fatal(err)
	}
	a.settle()
	if len(a.maxFake().Sent()) <= before {
		t.Fatal("no advertiser MAX delivery")
	}
	if r := c.do("DELETE", "/me/max-bot", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if maxConnected(t, c) {
		t.Fatal("disconnect ineffective")
	}
	// A replay of the consumed token cannot reconnect or change recipient.
	a.maxEvent(fmt.Sprintf("{\"update_type\":\"bot_started\",\"payload\":%q,\"user\":{\"user_id\":8102},\"timestamp\":1}", payload))
	a.settle()
	if maxConnected(t, c) {
		t.Fatal("replayed token reconnected")
	}
	if r := c.do("DELETE", "/me/max-bot", nil); r.status != 204 {
		t.Fatal("disconnect not idempotent")
	}
}
func TestMAXBotExpiredSupersededAndForgedTokens(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("max-expired@example.test")
	v := createMAXBot(t, c)
	u, _ := url.Parse(v.BotUrl)
	old := u.Query().Get("start")
	next := createMAXBot(t, c)
	a.startConnection(old, 8201)
	if maxConnected(t, c) {
		t.Fatal("superseded token accepted")
	}
	a.clk.Advance(10 * time.Minute)
	u, _ = url.Parse(next.BotUrl)
	a.startConnection(u.Query().Get("start"), 8201)
	if maxConnected(t, c) {
		t.Fatal("expired token accepted")
	}
	r := c.do("GET", "/me/max-bot/connections/"+next.Id.String(), nil)
	if !strings.Contains(string(r.body), "expired") {
		t.Fatal("expiry status", string(r.body))
	}
	if r := c.do("GET", "/me/max-bot/connections/"+uuid.NewString(), nil); r.status != 404 {
		t.Fatal("unknown connection", r.status)
	}
}

func TestMAXBotRateAndDisconnectCSRF(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("max-rate@example.test")
	for range 5 {
		createMAXBot(t, c)
	}
	if r := c.do("POST", "/me/max-bot/connections", nil); r.status != 429 {
		t.Fatal("unbounded MAX connection issuance", r.status)
	}
	c.csrf = false
	if r := c.do("DELETE", "/me/max-bot", nil); r.status != 403 {
		t.Fatal("disconnect accepted without CSRF", r.status)
	}
}
