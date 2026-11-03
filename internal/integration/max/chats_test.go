package max_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// routes — записанные ответы по методу и пути.
func routes(t *testing.T, m map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := m[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not.found","message":"chat.not.found"}`))
			return
		}
		_, _ = w.Write(testdata(t, name))
	}))
}

func TestGetChatAndMembership(t *testing.T) {
	srv := routes(t, map[string]string{
		"GET /chats/-70000000001":            "chat.json",
		"GET /chats/-70000000001/members/me": "membership.json",
	})
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	ctx := context.Background()
	chat, err := c.GetChat(ctx, -70000000001)
	if err != nil || chat.Type != max.ChatTypeChannel || !chat.IsPublic || chat.ParticipantsCount != 4200 ||
		chat.IconURL() == "" || chat.Link != "https://max.ru/recipes_daily" {
		t.Fatalf("chat %+v err %v", chat, err)
	}
	m, err := c.GetMembership(ctx, -70000000001)
	if err != nil || !m.IsAdmin || !slices.Contains(m.Permissions, "write") || !slices.Contains(m.Permissions, "delete") {
		t.Fatalf("membership %+v err %v", m, err)
	}
	if _, err := c.GetChat(ctx, 1); !job.IsPermanent(err) {
		t.Fatalf("unknown chat: want permanent, got %v", err)
	}
}

func TestDeleteMessage(t *testing.T) {
	var query string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write(testdata(t, "delete_ok.json"))
	}))
	defer ok.Close()
	c, _ := newClient(t, ok, nil)
	if err := c.DeleteMessage(context.Background(), -70000000001, "mid.code.0001"); err != nil {
		t.Fatal(err)
	}
	if query != "DELETE /messages?message_id=mid.code.0001" {
		t.Fatalf("request %q", query)
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testdata(t, "delete_false.json"))
	}))
	defer refused.Close()
	c2, _ := newClient(t, refused, nil)
	if err := c2.DeleteMessage(context.Background(), -70000000001, "mid.x"); !job.IsPermanent(err) || !strings.Contains(err.Error(), "message.not.found") {
		t.Fatalf("success=false: %v", err)
	}
}

func TestLeaveChat(t *testing.T) {
	srv := routes(t, map[string]string{"DELETE /chats/-70000000099/members/me": "delete_ok.json"})
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if err := c.LeaveChat(context.Background(), -70000000099); err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeHidesWebhookURL(t *testing.T) {
	const hook = "https://api.maxim.example/api/v1/webhooks/max/path-secret-value"
	srv := routes(t, map[string]string{
		"GET /subscriptions":  "subscriptions.json",
		"POST /subscriptions": "delete_ok.json",
	})
	defer srv.Close()
	c, d := newClient(t, srv, nil)
	ctx := context.Background()
	subs, err := c.ListSubscriptions(ctx)
	if err != nil || len(subs) != 1 || !strings.HasSuffix(subs[0].URL, "/path-secret") {
		t.Fatalf("subs %+v err %v", subs, err)
	}
	if err := c.Subscribe(ctx, hook, max.ChannelUpdateTypes, "header-secret-value"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Pool.Query(ctx, `SELECT request::text || coalesce(response_body::text, '') FROM outbound_request`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s, "path-secret") || strings.Contains(s, "header-secret-value") {
			t.Fatalf("webhook secret stored in outbound_request: %s", s)
		}
	}
}

func TestParseUpdates(t *testing.T) {
	dir := filepath.Join("testdata", "updates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 7 {
		t.Fatalf("only %d update samples", len(entries))
	}
	for _, e := range entries {
		u, err := max.ParseUpdate(testdata(t, filepath.Join("updates", e.Name())))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if u.ChatID == 0 {
			t.Errorf("%s: chat_id is empty", e.Name())
		}
	}
	added, _ := max.ParseUpdate(testdata(t, "updates/bot_added_channel.json"))
	if added.Type != max.UpdateBotAdded || !added.IsChannel || added.UserID() != 500100 {
		t.Fatalf("bot_added %+v", added)
	}
	code, _ := max.ParseUpdate(testdata(t, "updates/message_created_channel.json"))
	if !code.InChannel() || code.InDialog() || code.ChatID != -70000000001 || code.Message.Body.Text != "K7M2Q9XA" ||
		code.Message.Body.MID != "mid.code.0001" || code.SenderID() != 0 {
		t.Fatalf("channel message %+v", code)
	}
	dialog, _ := max.ParseUpdate(testdata(t, "updates/message_created_dialog.json"))
	if !dialog.InDialog() || dialog.SenderID() != 500100 {
		t.Fatalf("dialog message %+v", dialog)
	}
	title, _ := max.ParseUpdate(testdata(t, "updates/chat_title_changed.json"))
	if title.Title != "Рецепты каждый день" {
		t.Fatalf("title %+v", title)
	}
	if u, err := max.ParseUpdate([]byte(`{"update_type":"message_callback","timestamp":1}`)); err != nil || u.Type != "message_callback" {
		t.Fatalf("unknown type: %+v %v", u, err)
	}
	if _, err := max.ParseUpdate([]byte(`{"timestamp":1}`)); err == nil {
		t.Fatal("update without type accepted")
	}
	if _, err := max.ParseUpdate([]byte(`not json`)); err == nil {
		t.Fatal("invalid json accepted")
	}
}
