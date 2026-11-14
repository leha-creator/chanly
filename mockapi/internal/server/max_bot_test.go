package server_test

import (
	"net/http"
	"net/url"
	"testing"

	"maxim/mockapi/internal/api"
)

func startMaxBot(e *env, c *http.Client) api.MaxBotConnectionCreated {
	e.t.Helper()
	var v api.MaxBotConnectionCreated
	r := e.do(c, "POST", "/me/max-bot/connections", nil, "")
	if r.status != 201 {
		e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	r.json(e.t, &v)
	return v
}
func confirmMaxBot(e *env, v api.MaxBotConnectionCreated) {
	e.t.Helper()
	u, err := url.Parse(v.BotUrl)
	if err != nil {
		e.t.Fatal(err)
	}
	e.do(e.client(""), "POST", "/webhooks/max/test", map[string]any{"update_type": "bot_started", "payload": u.Query().Get("start"), "user": map[string]any{"user_id": 5001}}, "")
}
func TestMaxBotUserBinding(t *testing.T) {
	for _, email := range []string{adv, blg} {
		t.Run(email, func(t *testing.T) {
			e := newEnv(t)
			c := e.client(email)
			v := startMaxBot(e, c)
			var state api.MaxBot
			e.do(c, "GET", "/me/max-bot", nil, "").json(t, &state)
			if state.Connected {
				t.Fatal("connected before verified callback")
			}
			foreign := adv
			if email == adv {
				foreign = blg
			}
			if r := e.do(e.client(foreign), "GET", "/me/max-bot/connections/"+v.Id.String(), nil, ""); r.status != 404 {
				t.Fatalf("foreign: %d", r.status)
			}
			confirmMaxBot(e, v)
			e.do(c, "GET", "/me/max-bot", nil, "").json(t, &state)
			if !state.Connected {
				t.Fatal("not connected")
			}
			var settings api.AccountSettings
			e.do(c, "GET", "/me/settings", nil, "").json(t, &settings)
			found := false
			for _, option := range settings.Catalog {
				if option.Channel == "max_bot" {
					found = true
					if !option.Available {
						t.Fatalf("MAX unavailable after binding: %+v", option)
					}
				}
			}
			if !found {
				t.Fatal("MAX catalog missing")
			}
			var conn api.MaxBotConnection
			e.do(c, "GET", "/me/max-bot/connections/"+v.Id.String(), nil, "").json(t, &conn)
			if conn.Status != "connected" {
				t.Fatalf("status %s", conn.Status)
			}
			for range 2 {
				if r := e.do(c, "DELETE", "/me/max-bot", nil, ""); r.status != 204 {
					t.Fatalf("disconnect %d", r.status)
				}
			}
			confirmMaxBot(e, v)
			e.do(c, "GET", "/me/max-bot", nil, "").json(t, &state)
			if state.Connected {
				t.Fatal("replay restored disconnected binding")
			}
			e.do(c, "GET", "/me/settings", nil, "").json(t, &settings)
			for _, option := range settings.Catalog {
				if option.Channel == "max_bot" && option.Available {
					t.Fatalf("MAX still available after disconnect: %+v", option)
				}
			}
		})
	}
}
func TestMaxBotConnectionExpirationAndReplacement(t *testing.T) {
	e := newEnv(t)
	c := e.client(adv)
	old := startMaxBot(e, c)
	latest := startMaxBot(e, c)
	confirmMaxBot(e, old)
	var state api.MaxBot
	e.do(c, "GET", "/me/max-bot", nil, "").json(t, &state)
	if state.Connected {
		t.Fatal("superseded link accepted")
	}
	e.do(e.client(""), "POST", "/_mock/clock", map[string]any{"hours": 1}, "")
	confirmMaxBot(e, latest)
	var conn api.MaxBotConnection
	e.do(c, "GET", "/me/max-bot/connections/"+latest.Id.String(), nil, "").json(t, &conn)
	if conn.Status != "expired" {
		t.Fatalf("expired status %s", conn.Status)
	}
	e.do(c, "GET", "/me/max-bot", nil, "").json(t, &state)
	if state.Connected {
		t.Fatal("expired link accepted")
	}
}
