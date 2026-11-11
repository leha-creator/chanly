package server_test

import (
	"context"
	"testing"

	"maxim/mockapi/internal/store"
)

func TestChannelConnectByName(t *testing.T) {
	e := newEnv(t)
	tx, done := e.st.Begin(context.Background())
	ch := &store.Channel{ID: store.NewID(), MaxChatID: -911, Title: "Новый приватный канал", Link: "https://max.ru/named_test",
		Status: "unclaimed", IsPublic: false, BotIsAdmin: true, BotPermissions: []string{"write", "delete"}, RknStatus: "not_required", Prices: map[string]*store.Price{}}
	tx.D.Channels = append(tx.D.Channels, ch)
	done()
	c := e.client(blg)
	for _, name := range []string{"новый ПРИВАТНЫЙ канал", "@named_test", "https://max.ru/named_test/", "-911"} {
		r := e.do(c, "POST", "/blogger/channels/connections", map[string]any{"channel_name": name}, "")
		if r.status != 201 {
			t.Fatalf("create %d %s", r.status, r.body)
		}
		var v struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			ChannelID string  `json:"channel_id"`
			BotURL    *string `json:"bot_url"`
		}
		r.json(t, &v)
		if v.Status != "connected" || v.ChannelID != ch.ID.String() || v.BotURL != nil {
			t.Fatalf("named %+v", v)
		}
		if r = e.do(c, "GET", "/blogger/channels/connections/"+v.ID, nil, ""); r.status != 200 {
			t.Fatalf("get %d %s", r.status, r.body)
		}
	}
}

func TestChannelConnectByNameAmbiguousAndMissing(t *testing.T) {
	e := newEnv(t)
	tx, done := e.st.Begin(context.Background())
	for _, id := range []int64{-911, -912} {
		tx.D.Channels = append(tx.D.Channels, &store.Channel{ID: store.NewID(), MaxChatID: id, Title: "Два одинаковых названия", Status: "unclaimed", BotIsAdmin: true, BotPermissions: []string{"write", "delete"}})
	}
	done()
	c := e.client(blg)
	for name, reason := range map[string]string{"Два одинаковых названия": "channel_name_ambiguous", "Нет такого канала": "channel_not_found"} {
		r := e.do(c, "POST", "/blogger/channels/connections", map[string]any{"channel_name": name}, "")
		if r.status != 201 {
			t.Fatalf("create %d %s", r.status, r.body)
		}
		var v struct{ Status, Reason string }
		r.json(t, &v)
		if v.Status != "failed" || v.Reason != reason {
			t.Fatalf("failure %+v", v)
		}
	}
}
