package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/integration/max"
)

func createNamedConnection(t *testing.T, c *client, name string) uuid.UUID {
	t.Helper()
	r := c.do("POST", "/blogger/channels/connections", map[string]any{"channel_name": name})
	if r.status != 201 {
		t.Fatalf("create by name: %d %s", r.status, r.body)
	}
	if bytes.Contains(r.body, []byte(`"bot_url"`)) {
		t.Fatal("name connection issued a bot link")
	}
	var v apigen.ChannelConnection
	r.json(t, &v)
	if v.Status != "checking" || v.ChannelName == nil || *v.ChannelName != strings.TrimSpace(name) {
		t.Fatalf("created: %+v", v)
	}
	return v.Id
}

func TestChannelConnectByNameWithoutMAXIdentity(t *testing.T) {
	for _, input := range []string{"  канал БЕЗ технического поста  ", "@test", "https://max.ru/test/", "-911"} {
		t.Run(input, func(t *testing.T) {
			a := newTestApp(t)
			c, org := a.newBlogger("named@example.ru")
			chID := a.discoverConnectionChannel(-911, 5001)
			// Any attempted human membership check would fail this flow.
			a.mods.channel.UserMemberships = unavailableMemberships{}
			id := createNamedConnection(t, c, input)
			a.settle()
			v := getConnection(t, c, id)
			if v.Status != "connected" || v.ChannelId == nil || *v.ChannelId != chID {
				t.Fatalf("named connection: %+v", v)
			}
			var owner uuid.UUID
			if err := a.db.Pool.QueryRow(context.Background(), `SELECT organization_id FROM channel WHERE id=$1`, chID).Scan(&owner); err != nil || owner != org {
				t.Fatalf("owner %v %v", owner, err)
			}
			var identity bool
			if err := a.db.Pool.QueryRow(context.Background(), `SELECT max_user_id IS NOT NULL OR token_used_at IS NOT NULL FROM channel_connection WHERE id=$1`, id).Scan(&identity); err != nil || identity {
				t.Fatalf("unneeded MAX identity: %v %v", identity, err)
			}
			if len(a.maxFake().Sent()) != 0 {
				t.Fatal("name connection sent a message")
			}
			var deleted int
			_ = a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind='delete_max_message'`).Scan(&deleted)
			if deleted != 0 {
				t.Fatal("name connection scheduled a feed deletion")
			}
		})
	}
}

func TestChannelConnectByNameFailureAndRetry(t *testing.T) {
	a := newTestApp(t)
	c, _ := a.newBlogger("named.retry@example.ru")
	name := "Канал без технического поста"
	id := createNamedConnection(t, c, name)
	a.settle()
	if v := getConnection(t, c, id); v.Status != "failed" || v.Reason == nil || *v.Reason != "channel_not_found" {
		t.Fatalf("not found: %+v", v)
	}
	a.discoverConnectionChannel(-911, 5001)
	a.clk.Advance(11 * time.Second)
	path := "/blogger/channels/connections/" + id.String()
	for range 2 {
		if r := c.do("POST", path+"/refresh", nil); r.status != 202 {
			t.Fatalf("retry %d %s", r.status, r.body)
		}
	}
	a.settle()
	if v := getConnection(t, c, id); v.Status != "connected" {
		t.Fatalf("retry: %+v", v)
	}
}

func TestChannelConnectByNameRejectsAmbiguityAndOtherOrganization(t *testing.T) {
	a := newTestApp(t)
	c1, org1 := a.newBlogger("named.one@example.ru")
	c2, _ := a.newBlogger("named.two@example.ru")
	ch := a.discoverConnectionChannel(-911, 5001)
	a.discoverConnectionChannel(-912, 5002)
	id := createNamedConnection(t, c1, "Канал без технического поста")
	a.settle()
	if v := getConnection(t, c1, id); v.Status != "failed" || v.Reason == nil || *v.Reason != "channel_name_ambiguous" {
		t.Fatalf("ambiguous: %+v", v)
	}
	id1 := createNamedConnection(t, c1, "-911")
	id2 := createNamedConnection(t, c2, "-911")
	a.settle()
	v1, v2 := getConnection(t, c1, id1), getConnection(t, c2, id2)
	if !((v1.Status == "connected" && v2.Status == "failed") || (v2.Status == "connected" && v1.Status == "failed")) {
		t.Fatalf("race %+v %+v", v1, v2)
	}
	loser := v2
	if v2.Status == "connected" {
		loser = v1
	}
	if loser.Reason == nil || *loser.Reason != "linked_to_other" {
		t.Fatalf("loser: %+v", loser)
	}
	var owner uuid.UUID
	_ = a.db.Pool.QueryRow(context.Background(), `SELECT organization_id FROM channel WHERE id=$1`, ch).Scan(&owner)
	if v1.Status == "connected" && owner != org1 {
		t.Fatal("first ownership overwritten")
	}
	// Other users/organizations cannot inspect the connection session.
	if r := c2.do("GET", "/blogger/channels/connections/"+id1.String(), nil); r.status != 404 {
		t.Fatalf("foreign session %d", r.status)
	}
}

func TestChannelConnectByNameChecksBotAndFreshFacts(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*testApp)
	}{
		{"no_delete", "bot_rights_required", func(a *testApp) {
			a.maxFake().SetMembership(-911, max.Membership{IsAdmin: true, Permissions: []string{"write"}})
		}},
		{"renamed", "channel_changed", func(a *testApp) {
			a.maxFake().SetChat(max.Chat{ChatID: -911, Type: max.ChatTypeChannel, Title: "Другое имя"})
		}},
		{"not_channel", "not_channel", func(a *testApp) { a.maxFake().SetChat(max.Chat{ChatID: -911, Type: max.ChatTypeChat}) }},
		{"removed", "channel_unavailable", func(a *testApp) { a.maxEvent(`{"update_type":"bot_removed","chat_id":-911}`); a.settle() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			c, _ := a.newBlogger("named.rights@example.ru")
			a.discoverConnectionChannel(-911, 5001)
			tc.change(a)
			id := createNamedConnection(t, c, "Канал без технического поста")
			a.settle()
			if v := getConnection(t, c, id); v.Status != "failed" || v.Reason == nil || string(*v.Reason) != tc.reason {
				t.Fatalf("%s: %+v", tc.name, v)
			}
		})
	}
}

func TestChannelConnectByNamePrivateAndValidation(t *testing.T) {
	a := newTestApp(t)
	c, _ := a.newBlogger("named.private@example.ru")
	a.discoverConnectionChannel(-911, 5001)
	a.maxFake().SetChat(max.Chat{ChatID: -911, Type: max.ChatTypeChannel, Title: "Приватный канал", IsPublic: false})
	a.maxEvent(rightsEvent(-911))
	a.settle()
	id := createNamedConnection(t, c, "Приватный канал")
	a.settle()
	if v := getConnection(t, c, id); v.Status != "connected" {
		t.Fatalf("private: %+v", v)
	}
	for _, input := range []string{" ", "https://example.com/secret", strings.Repeat("я", 513)} {
		if r := c.do("POST", "/blogger/channels/connections", map[string]any{"channel_name": input}); r.status != 422 {
			t.Fatalf("invalid name %d %s", r.status, r.body)
		}
	}
	var n int
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM channel_connection`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("invalid names created sessions %d %v", n, err)
	}
}
