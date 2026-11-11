package server_test

import (
	"net/http"
	"net/url"
	"testing"
)

func prepareConnection(e *env) (string, string) {
	e.t.Helper()
	c := e.client(blg)
	var created struct {
		ID     string `json:"id"`
		BotURL string `json:"bot_url"`
	}
	r := e.do(c, http.MethodPost, "/blogger/channels/connections", nil, "")
	if r.status != 201 {
		e.t.Fatalf("create: %d %s", r.status, r.body)
	}
	r.json(e.t, &created)
	u, err := url.Parse(created.BotURL)
	if err != nil {
		e.t.Fatal(err)
	}
	e.do(e.client(""), http.MethodPost, "/webhooks/max/test", map[string]any{"update_type": "bot_started", "payload": u.Query().Get("start"), "user": map[string]any{"user_id": 5001}}, "")
	var ready struct {
		Status     string `json:"status"`
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	e.do(c, http.MethodGet, "/blogger/channels/connections/"+created.ID, nil, "").json(e.t, &ready)
	if ready.Status != "ready" || len(ready.Candidates) != 1 {
		e.t.Fatalf("ready: %+v", ready)
	}
	return created.ID, ready.Candidates[0].ID
}

func TestChannelConnectionWithoutFeed(t *testing.T) {
	e := newEnv(t)
	id, ch := prepareConnection(e)
	c := e.client(blg)
	path := "/blogger/channels/connections/" + id
	if r := e.do(e.client(adv), http.MethodGet, path, nil, ""); r.status != 403 {
		t.Fatalf("foreign cabinet: %d", r.status)
	}
	for range 2 {
		if r := e.do(c, http.MethodPost, path+"/claim", map[string]any{"channel_id": ch}, ""); r.status != 202 {
			t.Fatalf("claim %d %s", r.status, r.body)
		}
	}
	var v struct {
		Status    string `json:"status"`
		ChannelID string `json:"channel_id"`
	}
	e.do(c, http.MethodGet, path, nil, "").json(t, &v)
	if v.Status != "connected" || v.ChannelID != ch {
		t.Fatalf("connection %+v", v)
	}
}
