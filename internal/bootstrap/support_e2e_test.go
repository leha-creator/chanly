package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestSupportWebhookToMAXHTTP(t *testing.T) {
	cfg := testConfig()
	cfg.SupportMAX = SupportMAXConfig{Enabled: true, BotUserID: 100, WebhookPathSecret: testWebhookPath, WebhookSecret: testWebhookSecret}
	a := newTestAppWith(t, cfg)
	ctx := context.Background()
	operator := a.adminClient("support-operator@example.test", accountdomain.AdminModerator)
	const incoming = "Синтетический вопрос поддержки"
	const outgoing = "Синтетический ответ оператора"
	const botToken = "synthetic-support-token"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages" || r.URL.Query().Get("user_id") != "9007199254740999" || r.Header.Get("Authorization") != botToken {
			t.Error("invalid support provider request")
			w.WriteHeader(400)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Text != outgoing {
			t.Error("invalid support provider payload")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"body":{"mid":"mid.support.reply"}}}`)
	}))
	defer provider.Close()
	log := logx.Discard()
	out := outbound.New(a.db.Pool, a.clk, log, outbound.Options{System: "max_support", HTTP: provider.Client()})
	limiter := ratelimit.New(a.db.Pool, a.clk, log, ratelimit.Options{})
	a.mods.support.Provider = bridge.SupportMAX{Client: max.New(out, limiter, max.Options{BaseURL: provider.URL, Token: botToken, Namespace: "support"}, log)}
	webhook := func(secret string) int {
		raw := `{"update_type":"message_created","timestamp":1794990000000,"message":{"sender":{"user_id":9007199254740999,"name":"Synthetic user"},"recipient":{"chat_type":"dialog"},"body":{"mid":"mid.support.in","text":"` + incoming + `"}}}`
		req, _ := http.NewRequest(http.MethodPost, a.srv.URL+BaseURL+"/webhooks/max-support/"+testWebhookPath, strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Max-Bot-Api-Secret", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if webhook("wrong") != 401 || webhook(testWebhookSecret) != 200 || webhook(testWebhookSecret) != 200 {
		t.Fatal("webhook authentication or persistence failed")
	}
	for i := 0; i < 3; i++ {
		if _, err := a.worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r := operator.do(http.MethodGet, "/admin/support/conversations", nil)
	var list apigen.SupportConversationList
	r.json(t, &list)
	if r.status != 200 || len(list.Items) != 1 || list.Items[0].MaxUserId != "9007199254740999" || list.Items[0].Unread != 1 {
		t.Fatal("inbox lost or duplicated incoming")
	}
	conversation := list.Items[0]
	body := map[string]any{"text": outgoing, "attachments": []string{}, "client_key": ids.New(), "reply_to_sequence": conversation.LastSequence}
	r = operator.do(http.MethodPost, "/admin/support/conversations/"+conversation.Id.String()+"/messages", body)
	if r.status != 202 {
		t.Fatalf("send status: %d", r.status)
	}
	for i := 0; i < 3; i++ {
		if _, err := a.worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("support provider call count mismatch")
	}
	r = operator.do(http.MethodGet, "/admin/support/conversations/"+conversation.Id.String()+"/messages", nil)
	var history apigen.SupportMessageList
	r.json(t, &history)
	if r.status != 200 || len(history.Items) != 2 || string(history.Items[0].Status) != "sent" || history.Items[0].ExternalId != "mid.support.reply" {
		t.Fatal("reply outcome not recorded")
	}
	r = operator.do(http.MethodGet, "/admin/counters", nil)
	var counters apigen.AdminCounters
	r.json(t, &counters)
	if r.status != 200 || counters.SupportWaiting != 0 {
		t.Fatal("support counter not cleared")
	}
	operator.csrf = false
	if r = operator.do(http.MethodPost, "/admin/support/conversations/"+conversation.Id.String()+"/read", map[string]any{"sequence": 1}); r.status != 403 {
		t.Fatal("support mutation bypassed CSRF")
	}
	var technical string
	if err := a.db.Pool.QueryRow(ctx, `SELECT concat(
	 (SELECT string_agg(to_jsonb(o)::text,'') FROM outbound_request o WHERE system='max_support'),
	 (SELECT string_agg(payload::text,'') FROM job),
	 (SELECT string_agg(to_jsonb(a)::text,'') FROM audit_log a))`).Scan(&technical); err != nil {
		t.Fatal(err)
	}
	technical += a.logs.String()
	for _, secret := range []string{incoming, outgoing, botToken, "Synthetic user", testWebhookSecret, testWebhookPath} {
		if strings.Contains(technical, secret) {
			t.Fatal("support content or secret leaked into technical records")
		}
	}
}
