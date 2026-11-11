package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/apigen"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/integration/max"
)

func createConnection(t *testing.T, c *client) (uuid.UUID, string) {
	t.Helper()
	r := c.do(http.MethodPost, "/blogger/channels/connections", nil)
	if r.status != http.StatusCreated {
		t.Fatalf("create connection: %d %s", r.status, r.body)
	}
	var v struct {
		ID     uuid.UUID `json:"id"`
		BotURL string    `json:"bot_url"`
	}
	r.json(t, &v)
	u, err := url.Parse(v.BotURL)
	if err != nil {
		t.Fatal(err)
	}
	return v.ID, u.Query().Get("start")
}

func getConnection(t *testing.T, c *client, id uuid.UUID) apigen.ChannelConnection {
	t.Helper()
	r := c.do(http.MethodGet, "/blogger/channels/connections/"+id.String(), nil)
	if r.status != http.StatusOK {
		t.Fatalf("get connection: %d %s", r.status, r.body)
	}
	var v apigen.ChannelConnection
	r.json(t, &v)
	return v
}

func (a *testApp) discoverConnectionChannel(chatID, userID int64) uuid.UUID {
	a.t.Helper()
	fake := a.maxFake()
	fake.SetChat(max.Chat{ChatID: chatID, Type: max.ChatTypeChannel, Title: "Канал без технического поста", IsPublic: true, Link: "https://max.ru/test"})
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"write", "delete"}})
	fake.SetUserMembership(chatID, userID, max.Membership{IsAdmin: true, Permissions: []string{"write"}})
	// Actorless discovery is sufficient: it is not used as proof of ownership.
	a.maxEvent(fmt.Sprintf(`{"update_type":"bot_admin_permissions_changed","chat_id":%d,"is_channel":true}`, chatID))
	a.settle()
	var id uuid.UUID
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT id FROM channel WHERE max_chat_id=$1`, chatID).Scan(&id); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func (a *testApp) startConnection(payload string, userID int64) {
	a.t.Helper()
	a.maxEvent(fmt.Sprintf(`{"update_type":"bot_started","payload":%q,"user":{"user_id":%d}}`, payload, userID))
	a.settle()
}

func TestChannelConnectionWithoutFeedPost(t *testing.T) {
	a := newTestApp(t)
	c, org := a.newBlogger("connection@example.ru")
	chID := a.discoverConnectionChannel(-911, 5001)
	id, payload := createConnection(t, c)
	path := "/blogger/channels/connections/" + id.String()
	if r := c.do(http.MethodPost, path+"/refresh", nil); r.status != 409 {
		t.Fatalf("unverified refresh: %d", r.status)
	}
	if v := getConnection(t, c, id); v.Status != "pending" || len(v.Candidates) != 0 {
		t.Fatalf("pending %+v", v)
	}
	a.startConnection(payload, 5001)
	v := getConnection(t, c, id)
	if v.Status != "ready" || len(v.Candidates) != 1 || v.Candidates[0].Id != chID {
		t.Fatalf("candidates %+v", v)
	}
	// A replay, including a different MAX user, cannot replace the identity.
	a.startConnection(payload, 5002)
	var maxID int64
	var stored string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT max_user_id FROM channel_connection WHERE id=$1`, id).Scan(&maxID); err != nil || maxID != 5001 {
		t.Fatalf("identity %d %v", maxID, err)
	}
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT payload::text FROM inbound_event WHERE event_type='bot_started' LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, payload) || strings.Contains(a.logs.String(), payload) {
		t.Fatal("bearer token persisted or logged")
	}
	for range 2 {
		if r := c.do(http.MethodPost, path+"/claim", map[string]any{"channel_id": chID}); r.status != 202 {
			t.Fatalf("claim %d %s", r.status, r.body)
		}
	}
	a.settle()
	if v = getConnection(t, c, id); v.Status != "connected" || v.ChannelId == nil || *v.ChannelId != chID {
		t.Fatalf("connected %+v", v)
	}
	var owner uuid.UUID
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT organization_id FROM channel WHERE id=$1`, chID).Scan(&owner); err != nil || owner != org {
		t.Fatalf("owner %v %v", owner, err)
	}
	// No create/delete message job is part of the new flow.
	var n int
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind=$1`, channelapp.KindDeleteMaxMessage).Scan(&n); err != nil || n != 0 {
		t.Fatalf("message deletion %d %v", n, err)
	}
	if len(a.maxFake().Sent()) != 0 {
		t.Fatal("onboarding sent a message")
	}
	if r := c.do(http.MethodPost, path+"/claim", map[string]any{"channel_id": chID}); r.status != 202 {
		t.Fatalf("connected retry %d", r.status)
	}
}

func TestChannelConnectionIsolationAndExpiry(t *testing.T) {
	a := newTestApp(t)
	c, org := a.newBlogger("connection.owner@example.ru")
	other, _ := a.newBlogger("connection.other@example.ru")
	id, payload := createConnection(t, c)
	path := "/blogger/channels/connections/" + id.String()
	for _, op := range []struct {
		method, suffix string
		body           any
	}{{"GET", "", nil}, {"POST", "/refresh", nil}, {"POST", "/claim", map[string]any{"channel_id": uuid.New()}}} {
		if r := other.do(op.method, path+op.suffix, op.body); r.status != 404 {
			t.Fatalf("foreign %s: %d %s", op.suffix, r.status, r.body)
		}
	}
	var ownUser uuid.UUID
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT user_id FROM channel_connection WHERE id=$1`, id).Scan(&ownUser); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mods.channel.GetConnection(context.Background(), org, uuid.New(), id); err == nil {
		t.Fatal("another user in organization read session")
	}
	if ownUser == uuid.Nil {
		t.Fatal("session has no owner")
	}
	a.clk.Advance(2 * time.Hour)
	a.startConnection(payload, 5001)
	if v := getConnection(t, c, id); v.Status != "expired" || len(v.Candidates) != 0 {
		t.Fatalf("expired %+v", v)
	}
	if r := c.do(http.MethodPost, path+"/refresh", nil); r.status != 409 {
		t.Fatalf("expired refresh %d", r.status)
	}
}

func TestChannelConnectionRechecksRights(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		revoke       func(*testApp)
	}{
		{"user", "user_rights_required", func(a *testApp) { a.maxFake().SetUserMembership(-911, 5001, max.Membership{}) }},
		{"bot", "bot_rights_required", func(a *testApp) {
			a.maxFake().SetMembership(-911, max.Membership{IsAdmin: true, Permissions: []string{"write"}})
		}},
		{"removed", "channel_unavailable", func(a *testApp) { a.maxEvent(`{"update_type":"bot_removed","chat_id":-911}`); a.settle() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			c, _ := a.newBlogger("rights@example.ru")
			chID := a.discoverConnectionChannel(-911, 5001)
			id, payload := createConnection(t, c)
			a.startConnection(payload, 5001)
			tc.revoke(a)
			r := c.do("POST", "/blogger/channels/connections/"+id.String()+"/claim", map[string]any{"channel_id": chID})
			if r.status != 202 {
				t.Fatalf("claim %d %s", r.status, r.body)
			}
			a.settle()
			v := getConnection(t, c, id)
			if v.Status != "failed" || v.Reason == nil || string(*v.Reason) != tc.reason {
				t.Fatalf("revoked %+v", v)
			}
			var n int
			_ = a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM channel WHERE id=$1 AND organization_id IS NOT NULL`, chID).Scan(&n)
			if n != 0 {
				t.Fatal("channel linked after rights revoked")
			}
		})
	}
}

func TestChannelConnectionConcurrentOrganizations(t *testing.T) {
	a := newTestApp(t)
	c1, org1 := a.newBlogger("connection.one@example.ru")
	c2, org2 := a.newBlogger("connection.two@example.ru")
	chID := a.discoverConnectionChannel(-911, 5001)
	a.maxFake().SetUserMembership(-911, 5002, max.Membership{IsOwner: true})
	id1, p1 := createConnection(t, c1)
	id2, p2 := createConnection(t, c2)
	a.startConnection(p1, 5001)
	a.startConnection(p2, 5002)
	a.mods.channel.UserMemberships = &barrierMemberships{base: a.mods.channel.UserMemberships, release: make(chan struct{})}
	for _, v := range []struct {
		c  *client
		id uuid.UUID
	}{{c1, id1}, {c2, id2}} {
		if r := v.c.do("POST", "/blogger/channels/connections/"+v.id.String()+"/claim", map[string]any{"channel_id": chID}); r.status != 202 {
			t.Fatalf("claim %d %s", r.status, r.body)
		}
	}
	a.settle()
	v1, v2 := getConnection(t, c1, id1), getConnection(t, c2, id2)
	winner, loser, winnerOrg := v1, v2, org1
	if v2.Status == "connected" {
		winner, loser, winnerOrg = v2, v1, org2
	}
	if winner.Status != "connected" || loser.Status != "failed" || loser.Reason == nil || *loser.Reason != "linked_to_other" {
		t.Fatalf("concurrent claims: %+v %+v", v1, v2)
	}
	var owner uuid.UUID
	_ = a.db.Pool.QueryRow(context.Background(), `SELECT organization_id FROM channel WHERE id=$1`, chID).Scan(&owner)
	if owner != winnerOrg {
		t.Fatal("ownership overwritten")
	}
}

type barrierMemberships struct {
	base    channeldomain.UserMembershipReader
	arrived atomic.Int32
	release chan struct{}
}

func (b *barrierMemberships) GetUserMembership(ctx context.Context, chatID, userID int64) (channeldomain.Membership, error) {
	if b.arrived.Add(1) == 2 {
		close(b.release)
	}
	select {
	case <-b.release:
		return b.base.GetUserMembership(ctx, chatID, userID)
	case <-ctx.Done():
		return channeldomain.Membership{}, ctx.Err()
	}
}

func TestChannelConnectionCandidatePagination(t *testing.T) {
	a := newTestApp(t)
	c, _ := a.newBlogger("connection.pages@example.ru")
	excluded := map[uuid.UUID]bool{}
	for i := range 24 {
		chatID := int64(-1000 - i)
		ch := a.discoverConnectionChannel(chatID, 5001)
		switch i {
		case 21:
			a.maxFake().SetUserMembership(chatID, 5001, max.Membership{})
			excluded[ch] = true
		case 22:
			a.maxFake().SetMembership(chatID, max.Membership{})
			excluded[ch] = true
		case 23:
			a.maxFake().SetChat(max.Chat{ChatID: chatID, Type: max.ChatTypeChannel, Title: "Закрытый канал"})
			excluded[ch] = true
		}
	}
	id, payload := createConnection(t, c)
	a.startConnection(payload, 5001)
	v := getConnection(t, c, id)
	if v.Status != "ready" || len(v.Candidates) != 21 {
		t.Fatalf("paginated candidates: %s %d", v.Status, len(v.Candidates))
	}
	for _, item := range v.Candidates {
		if excluded[item.Id] {
			t.Fatal("unverified candidate exposed")
		}
	}
	if r := c.do("POST", "/blogger/channels/connections/"+id.String()+"/claim", map[string]any{"channel_id": uuid.New()}); r.status != 404 {
		t.Fatalf("unknown candidate %d", r.status)
	}
}

type unavailableMemberships struct{}

func (unavailableMemberships) GetUserMembership(context.Context, int64, int64) (channeldomain.Membership, error) {
	return channeldomain.Membership{}, errors.New("MAX unavailable")
}

func TestChannelConnectionFailureAndRefresh(t *testing.T) {
	a := newTestApp(t)
	c, _ := a.newBlogger("connection.retry@example.ru")
	chID := a.discoverConnectionChannel(-911, 5001)
	original := a.mods.channel.UserMemberships
	a.mods.channel.UserMemberships = unavailableMemberships{}
	id, payload := createConnection(t, c)
	a.startConnection(payload, 5001)
	if v := getConnection(t, c, id); v.Status != "failed" || v.Reason == nil || *v.Reason != "max_unavailable" {
		t.Fatalf("failure %+v", v)
	}
	a.mods.channel.UserMemberships = original
	a.clk.Advance(11 * time.Second)
	path := "/blogger/channels/connections/" + id.String()
	for range 2 {
		if r := c.do("POST", path+"/refresh", nil); r.status != 202 {
			t.Fatalf("refresh %d %s", r.status, r.body)
		}
	}
	a.settle()
	if v := getConnection(t, c, id); v.Status != "ready" || len(v.Candidates) != 1 || v.Candidates[0].Id != chID {
		t.Fatalf("recovery %+v", v)
	}
}
