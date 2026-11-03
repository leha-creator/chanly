package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
)

func (e *env) eventStatus(eventType string) []string {
	e.t.Helper()
	rows, err := e.d.Pool.Query(context.Background(), `SELECT status FROM inbound_event WHERE event_type = $1 ORDER BY created_at, id`, eventType)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestBotAddedIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.max.set(-100, publicCh, allRights)
	body := botAdded(-100, 500100)
	e.event(body)
	e.event(body) // тот же Webhook повторно
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel`); n != 1 {
		t.Fatalf("channels = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind = $1`, app.KindRefreshChannelInfo); n != 1 {
		t.Fatalf("refresh jobs = %d", n)
	}
	c := e.channel(-100)
	if c.Status != domain.StatusUnclaimed || c.AddedByMaxUserID != 500100 || c.Title != "Рецепты" ||
		c.SubscribersCount != 4200 || c.BotPermissionsCheckedAt == nil || !c.IsPublic {
		t.Fatalf("channel %+v", c)
	}
}

func TestRightsAndStatuses(t *testing.T) {
	e := newEnv(t)
	noDelete := domain.Membership{IsAdmin: true, Permissions: []string{"write"}}
	c, _ := e.linkedChannel(-100, noDelete)
	if c.Status != domain.StatusPendingRights || c.StatusReason != domain.ReasonMissingRights ||
		len(c.MissingPermissions()) != 1 || c.MissingPermissions()[0] != "delete" {
		t.Fatalf("pending_rights: %+v", c)
	}
	e.max.set(-100, publicCh, allRights)
	e.event(rightsChanged(-100))
	e.drain()
	if s := e.channel(-100).Status; s != domain.StatusModeration {
		t.Fatalf("after rights granted: %s", s)
	}

	// Одобрение — этап «Каталог»; здесь записываем его напрямую.
	channeltest.Approve(t, e.d.Pool, c.ID)
	e.max.set(-100, publicCh, noDelete)
	e.event(rightsChanged(-100))
	e.drain()
	got := e.channel(-100)
	if got.Status != domain.StatusSuspended || len(e.avail.lost) != 1 || e.avail.lost[0].From != domain.StatusActive {
		t.Fatalf("suspended: %s, lost %+v", got.Status, e.avail.lost)
	}
	e.max.set(-100, publicCh, allRights)
	e.event(rightsChanged(-100))
	e.drain()
	if s := e.channel(-100).Status; s != domain.StatusActive || len(e.avail.restored) != 1 {
		t.Fatalf("restored: %s, %d", s, len(e.avail.restored))
	}

	e.event(botRemoved(-100))
	e.drain()
	if s := e.channel(-100).Status; s != domain.StatusDisconnected || len(e.avail.lost) != 2 {
		t.Fatalf("removed: %s", s)
	}
	e.event(botAdded(-100, 500200))
	e.drain()
	back := e.channel(-100)
	if back.Status != domain.StatusActive || back.AddedByMaxUserID != 500200 || *back.OrganizationID != *c.OrganizationID {
		t.Fatalf("re-added: %+v", back)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.status_changed' AND object_id = $1`, c.ID.String()); n < 6 {
		t.Fatalf("status_changed audit entries = %d", n)
	}
}

func TestClosedChannelAndNonChannel(t *testing.T) {
	e := newEnv(t)
	closed := publicCh
	closed.IsPublic = false
	org := e.org()
	issued, _ := e.svc.CreateLinkCode(e.ctx(), org)
	e.max.set(-100, closed, allRights)
	e.event(botAdded(-100, 1))
	e.drain()
	e.event(channelPost(-100, "mid.1", issued.Code))
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusPendingRights || c.StatusReason != domain.ReasonNotPublic {
		t.Fatalf("closed channel: %s/%s", c.Status, c.StatusReason)
	}

	e.event(`{"update_type":"bot_added","timestamp":1,"chat_id":-200,"user":{"user_id":1},"is_channel":false}`)
	e.drain()
	if !contains(e.max.left, -200) || e.count(`SELECT count(*) FROM channel WHERE max_chat_id = -200`) != 0 {
		t.Fatalf("group chat: left %v", e.max.left)
	}
	e.event(botRemoved(-300))
	e.drain()
	if st := e.eventStatus("bot_removed"); len(st) != 1 || st[0] != "ignored" {
		t.Fatalf("unknown chat bot_removed: %v", st)
	}
}

func TestPrivateChannelDevelopmentPolicy(t *testing.T) {
	e := newEnv(t)
	e.svc.Config.Visibility = domain.AllowPrivateChannels
	closed := publicCh
	closed.IsPublic = false
	org := e.org()
	issued, err := e.svc.CreateLinkCode(e.ctx(), org)
	if err != nil {
		t.Fatal(err)
	}
	e.max.set(-100, closed, allRights)
	e.event(botAdded(-100, 1))
	e.drain()
	e.event(channelPost(-100, "mid.private", issued.Code))
	e.drain()
	c := e.channel(-100)
	if c.Status != domain.StatusModeration || c.IsPublic {
		t.Fatalf("private channel eligibility/fact: %s public=%v", c.Status, c.IsPublic)
	}
	e.max.set(-100, closed, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	e.event(rightsChanged(-100))
	e.drain()
	c = e.channel(-100)
	if c.Status != domain.StatusPendingRights || c.StatusReason != domain.ReasonMissingRights {
		t.Fatalf("private policy bypassed bot rights: %s/%s", c.Status, c.StatusReason)
	}
}

func TestRightsDiscoverChannelWithoutBotAdded(t *testing.T) {
	e := newEnv(t)
	e.svc.Config.Visibility = domain.AllowPrivateChannels
	closed := publicCh
	closed.IsPublic = false
	e.max.set(-100, closed, allRights)
	org := e.org()
	issued, err := e.svc.CreateLinkCode(e.ctx(), org)
	if err != nil {
		t.Fatal(err)
	}
	e.event(channelPost(-100, "mid.early-rights", issued.Code))
	e.drain()
	if st := e.eventStatus("message_created"); len(st) != 1 || st[0] != "received" {
		t.Fatalf("early code: %v", st)
	}
	// Real MAX channel rights updates can omit user and bot_added entirely.
	body := `{"update_type":"bot_admin_permissions_changed","timestamp":1,"chat_id":-100,"is_channel":true}`
	e.event(body)
	e.event(body)
	e.drain()
	c := e.channel(-100)
	if c.Status != domain.StatusUnclaimed || c.OrganizationID != nil || c.AddedByMaxUserID != 0 || c.BotPermissionsCheckedAt == nil {
		t.Fatalf("discovery: %+v", c)
	}
	if n := e.count(`SELECT count(*) FROM channel`); n != 1 {
		t.Fatalf("channels: %d", n)
	}
	e.clk.Advance(time.Minute)
	e.drain()
	c = e.channel(-100)
	if c.Status != domain.StatusModeration || c.IsPublic || c.OrganizationID == nil || *c.OrganizationID != org || !contains(e.max.deleted, "mid.early-rights") {
		t.Fatalf("linked: %+v", c)
	}
	if n := e.count(`SELECT count(*) FROM auth_code WHERE purpose = 'channel_link' AND consumed_at IS NULL`); n != 0 {
		t.Fatalf("live codes: %d", n)
	}
	e.event(rightsChanged(-100))
	e.drain()
	if c = e.channel(-100); c.AddedByMaxUserID != 0 || *c.OrganizationID != org {
		t.Fatal("rights update changed ownership")
	}
	e.event(botRemoved(-100))
	e.drain()
	e.event(rightsChanged(-100))
	e.drain()
	if c = e.channel(-100); c.Status != domain.StatusDisconnected {
		t.Fatalf("stale rights revived removed channel: %s", c.Status)
	}
	e.event(`{"update_type":"bot_admin_permissions_changed","timestamp":2,"chat_id":-200,"is_channel":false}`)
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel WHERE max_chat_id = -200`); n != 0 {
		t.Fatal("nonchannel discovered")
	}
}

func TestLinkCodeRules(t *testing.T) {
	e := newEnv(t)
	org := e.org()
	issued, _ := e.svc.CreateLinkCode(e.ctx(), org)
	e.max.set(-100, publicCh, allRights)
	e.event(botAdded(-100, 1))
	e.drain()

	// Обычный пост и неверный код канал не трогают.
	e.event(channelPost(-100, "mid.post", "Новый рецепт борща"))
	e.event(channelPost(-100, "mid.wrong", "ZZZZZZZZ"))
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusUnclaimed || len(e.max.deleted) != 0 {
		t.Fatalf("wrong code: %s, deleted %v", c.Status, e.max.deleted)
	}
	// Код в нижнем регистре с пробелами подходит.
	e.event(channelPost(-100, "mid.code", "  "+strings.ToLower(issued.Code)+"\n"))
	e.drain()
	c := e.channel(-100)
	if c.Status != domain.StatusModeration || *c.OrganizationID != org || !contains(e.max.deleted, "mid.code") {
		t.Fatalf("linked: %s, deleted %v", c.Status, e.max.deleted)
	}
	if n := e.count(`SELECT count(*) FROM auth_code WHERE purpose = 'channel_link' AND consumed_at IS NULL`); n != 0 {
		t.Fatalf("live codes after link = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.linked'`); n != 1 {
		t.Fatalf("channel.linked = %d", n)
	}

	// Код другой организации в подключённом канале: владелец прежний, оповещение.
	other := e.org()
	foreign, _ := e.svc.CreateLinkCode(e.ctx(), other)
	e.event(channelPost(-100, "mid.foreign", foreign.Code))
	e.drain()
	if c := e.channel(-100); *c.OrganizationID != org {
		t.Fatal("owner changed by a foreign code")
	}
	if len(e.alerts.list) != 1 || e.alerts.list[0].Reason != "channel_link_conflict" || !contains(e.max.deleted, "mid.foreign") {
		t.Fatalf("conflict: alerts %+v deleted %v", e.alerts.list, e.max.deleted)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.link_rejected'`); n != 1 {
		t.Fatalf("link_rejected = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM auth_code WHERE purpose = 'channel_link' AND consumed_at IS NULL AND organization_id = $1`, other); n != 1 {
		t.Fatal("foreign code consumed")
	}
	if strings.Contains(e.logs.String(), issued.Code) || strings.Contains(e.logs.String(), foreign.Code) {
		t.Fatal("link code written to the log")
	}
}

func TestExpiredCode(t *testing.T) {
	e := newEnv(t)
	issued, _ := e.svc.CreateLinkCode(e.ctx(), e.org())
	e.max.set(-100, publicCh, allRights)
	e.event(botAdded(-100, 1))
	e.drain()
	e.clk.Advance(61 * time.Minute)
	e.event(channelPost(-100, "mid.late", issued.Code))
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusUnclaimed {
		t.Fatalf("expired code linked the channel: %s", c.Status)
	}
}

func TestCodeBeforeBotAdded(t *testing.T) {
	e := newEnv(t)
	issued, _ := e.svc.CreateLinkCode(e.ctx(), e.org())
	e.max.set(-100, publicCh, allRights)
	e.event(channelPost(-100, "mid.early", issued.Code))
	e.drain()
	if st := e.eventStatus("message_created"); len(st) != 1 || st[0] != "received" {
		t.Fatalf("early code event: %v", st)
	}
	e.event(botAdded(-100, 1))
	e.drain()
	e.clk.Advance(time.Minute)
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusModeration {
		t.Fatalf("after retry: %s", c.Status)
	}
	if st := e.eventStatus("message_created"); st[0] != "processed" {
		t.Fatalf("early code event after retry: %v", st)
	}
}

func TestCodeInUnknownChannelGivesUp(t *testing.T) {
	e := newEnv(t)
	issued, _ := e.svc.CreateLinkCode(e.ctx(), e.org())
	e.event(channelPost(-100, "mid.early", issued.Code))
	for range 7 {
		e.drain()
		e.clk.Advance(time.Minute)
	}
	e.drain()
	if st := e.eventStatus("message_created"); len(st) != 1 || st[0] != "ignored" {
		t.Fatalf("code in unknown channel: %v", st)
	}
}

func TestDialogReplyAndTitle(t *testing.T) {
	e := newEnv(t)
	e.event(dialogMessage(500100, "Привет"))
	e.event(dialogMessage(500100, "Как подключить канал?"))
	e.drain()
	e.clk.Advance(10 * time.Second)
	e.drain()
	if got := e.max.sent[500100]; len(got) != 1 || got[0] != app.DialogReplyText {
		t.Fatalf("dialog replies: %v", got)
	}

	e.max.set(-100, publicCh, allRights)
	e.event(botAdded(-100, 1))
	e.drain()
	e.event(`{"update_type":"chat_title_changed","timestamp":7,"chat_id":-100,"title":"Новое имя"}`)
	e.drain()
	if c := e.channel(-100); c.Title != "Новое имя" {
		t.Fatalf("title %q", c.Title)
	}
	e.event(`{"update_type":"user_added","timestamp":8,"chat_id":-100}`)
	e.drain()
	if st := e.eventStatus("user_added"); len(st) != 1 || st[0] != "ignored" {
		t.Fatalf("user_added: %v", st)
	}
}

func TestReleaseUnclaimed(t *testing.T) {
	e := newEnv(t)
	e.max.set(-100, publicCh, allRights)
	e.max.set(-101, publicCh, allRights)
	e.event(botAdded(-100, 1))
	e.drain()
	e.clk.Advance(24 * time.Hour)
	e.event(botAdded(-101, 1))
	e.drain()

	e.clk.Advance(6*24*time.Hour + time.Minute) // -100 ждёт 7 суток и минуту, -101 — 6 суток
	if err := e.svc.ReleaseUnclaimed(e.ctx()); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusDisconnected || c.StatusReason != domain.ReasonUnclaimedExpired {
		t.Fatalf("expired channel: %s/%s", c.Status, c.StatusReason)
	}
	if c := e.channel(-101); c.Status != domain.StatusUnclaimed {
		t.Fatalf("fresh channel: %s", c.Status)
	}
	if !contains(e.max.left, -100) || contains(e.max.left, -101) {
		t.Fatalf("left %v", e.max.left)
	}
	// Бота снова добавили — канал снова ждёт кода.
	e.event(botAdded(-100, 2))
	e.drain()
	if c := e.channel(-100); c.Status != domain.StatusUnclaimed || c.UnclaimedSince == nil {
		t.Fatalf("re-added: %s", c.Status)
	}
}

// postListener запоминает сообщения, переданные модулю размещений (R-9).
type postListener struct {
	mu    sync.Mutex
	posts []domain.ChannelPost
}

func (l *postListener) ChannelPostCreated(_ context.Context, p domain.ChannelPost) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.posts = append(l.posts, p)
	return nil
}

func (l *postListener) ids() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, p := range l.posts {
		out = append(out, p.MessageID)
	}
	return out
}

func TestChannelPostsPassedToPlacements(t *testing.T) {
	e := newEnv(t)
	l := &postListener{}
	e.svc.SetPostListener(l)
	org := e.org()
	issued, _ := e.svc.CreateLinkCode(e.ctx(), org)
	e.max.set(-100, publicCh, allRights)
	e.event(botAdded(-100, 1))
	e.drain()

	e.event(`{"update_type":"message_created","timestamp":1792141300000,"message":{"sender":{"user_id":42},` +
		`"recipient":{"chat_id":-100,"chat_type":"channel"},"timestamp":1792141200000,"body":{"mid":"mid.human","text":"Новый рецепт"}}}`)
	e.event(channelPost(-100, "mid.wrong", "ZZZZZZZZ"))
	e.event(channelPost(-100, "mid.code", issued.Code))
	e.event(channelPost(-999, "mid.unknown", "Пост в чужом канале"))
	e.drain()

	got := l.ids()
	if len(got) != 2 || !contains(got, "mid.human") || !contains(got, "mid.wrong") {
		t.Fatalf("passed %v", got)
	}
	for _, p := range l.posts {
		if p.MessageID == "mid.human" && (p.SenderID != 42 || !p.At.Equal(time.UnixMilli(1792141200000)) ||
			p.InboundEventID == nil || p.ChannelID != e.channel(-100).ID) {
			t.Fatalf("post %+v", p)
		}
	}
	if st := e.eventStatus("message_created"); !contains(st, "ignored") {
		t.Fatalf("unknown channel post: %v", st)
	}
}
