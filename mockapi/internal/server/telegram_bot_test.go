package server_test

import (
	"net/http"
	"net/url"
	"testing"

	"maxim/mockapi/internal/api"
)

func startTelegramBot(e *env, c *http.Client) api.TelegramBotConnectionCreated {
	e.t.Helper()
	var v api.TelegramBotConnectionCreated
	r := e.do(c, "POST", "/me/telegram-bot/connections", nil, "")
	if r.status != 201 {
		e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	r.json(e.t, &v)
	return v
}
func confirmTelegramBot(e *env, v api.TelegramBotConnectionCreated) {
	e.t.Helper()
	u, err := url.Parse(v.BotUrl)
	if err != nil {
		e.t.Fatal(err)
	}
	e.do(e.client(""), "POST", "/webhooks/telegram", map[string]any{"update_id": 1, "message": map[string]any{"text": "/start " + u.Query().Get("start"), "from": map[string]any{"id": 5001}, "chat": map[string]any{"id": 5001, "type": "private"}}}, "")
}
func TestTelegramBotUserBinding(t *testing.T) {
	for _, email := range []string{adv, blg} {
		t.Run(email, func(t *testing.T) {
			e := newEnv(t)
			c := e.client(email)
			v := startTelegramBot(e, c)
			var state api.TelegramBot
			e.do(c, "GET", "/me/telegram-bot", nil, "").json(t, &state)
			if state.Connected {
				t.Fatal("connected before verified callback")
			}
			foreign := adv
			if email == adv {
				foreign = blg
			}
			if r := e.do(e.client(foreign), "GET", "/me/telegram-bot/connections/"+v.Id.String(), nil, ""); r.status != 404 {
				t.Fatalf("foreign: %d", r.status)
			}
			confirmTelegramBot(e, v)
			e.do(c, "GET", "/me/telegram-bot", nil, "").json(t, &state)
			if !state.Connected {
				t.Fatal("not connected")
			}
			var settings api.AccountSettings
			e.do(c, "GET", "/me/settings", nil, "").json(t, &settings)
			found := false
			for _, option := range settings.Catalog {
				if option.Channel == "telegram_bot" {
					found = true
					if !option.Available {
						t.Fatalf("Telegram unavailable after binding: %+v", option)
					}
				}
			}
			if !found {
				t.Fatal("Telegram catalog missing")
			}
			var conn api.TelegramBotConnection
			e.do(c, "GET", "/me/telegram-bot/connections/"+v.Id.String(), nil, "").json(t, &conn)
			if conn.Status != "connected" {
				t.Fatalf("status %s", conn.Status)
			}
			for range 2 {
				if r := e.do(c, "DELETE", "/me/telegram-bot", nil, ""); r.status != 204 {
					t.Fatalf("disconnect %d", r.status)
				}
			}
			confirmTelegramBot(e, v)
			e.do(c, "GET", "/me/telegram-bot", nil, "").json(t, &state)
			if state.Connected {
				t.Fatal("replay restored disconnected binding")
			}
			e.do(c, "GET", "/me/settings", nil, "").json(t, &settings)
			for _, option := range settings.Catalog {
				if option.Channel == "telegram_bot" && option.Available {
					t.Fatalf("Telegram still available after disconnect: %+v", option)
				}
			}
		})
	}
}
func TestTelegramBotConnectionExpirationAndReplacement(t *testing.T) {
	e := newEnv(t)
	c := e.client(adv)
	old := startTelegramBot(e, c)
	latest := startTelegramBot(e, c)
	confirmTelegramBot(e, old)
	var state api.TelegramBot
	e.do(c, "GET", "/me/telegram-bot", nil, "").json(t, &state)
	if state.Connected {
		t.Fatal("superseded link accepted")
	}
	e.do(e.client(""), "POST", "/_mock/clock", map[string]any{"hours": 1}, "")
	confirmTelegramBot(e, latest)
	var conn api.TelegramBotConnection
	e.do(c, "GET", "/me/telegram-bot/connections/"+latest.Id.String(), nil, "").json(t, &conn)
	if conn.Status != "expired" {
		t.Fatalf("expired status %s", conn.Status)
	}
	e.do(c, "GET", "/me/telegram-bot", nil, "").json(t, &state)
	if state.Connected {
		t.Fatal("expired link accepted")
	}
}
