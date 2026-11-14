package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
)

// bloggerClient — пользователь с организацией-блогером (S-10) и исполнителем Рокет Ворк active.
func (a *testApp) bloggerClient(email string) *client {
	a.t.Helper()
	c, org := a.newBlogger(email)
	a.activeContractor(org)
	return c
}

// newBlogger — самозанятый блогер без онбординга в Рокет Ворк.
func (a *testApp) newBlogger(email string) (*client, uuid.UUID) {
	a.t.Helper()
	c := a.client()
	c.login(email)
	r := c.do(http.MethodPost, "/organizations", map[string]any{
		"cabinet": "blogger", "legal_type": "self_employed", "inn": accounttest.INN(domain.SelfEmployed),
		"name": "Анна Блогерова", "contact_name": "Анна Блогерова", "phone": "+79990001122",
		"tax_regime": "npd", "vat_rate": "none",
	})
	if r.status != http.StatusCreated {
		a.t.Fatalf("create blogger: %d %s", r.status, r.body)
	}
	var org struct {
		ID uuid.UUID `json:"id"`
	}
	r.json(a.t, &org)
	return c, org.ID
}

func (a *testApp) maxFake() *maxfake.Client { return a.mods.in.max.(*maxfake.Client) }

// webhook отправляет событие MAX на приём, как это делает MAX.
func (a *testApp) webhook(path, secret, body string) int {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/webhooks/max/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(max.SecretHeader, secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

var eventSeq int

func (a *testApp) maxEvent(body string) {
	a.t.Helper()
	eventSeq++
	body = strings.Replace(body, "{", fmt.Sprintf(`{"timestamp":%d,`, 1760600000000+eventSeq), 1)
	if code := a.webhook(testWebhookPath, testWebhookSecret, body); code != http.StatusOK {
		a.t.Fatalf("webhook: %d", code)
	}
}

// settle выполняет готовые задания, пока они есть.
func (a *testApp) settle() {
	a.t.Helper()
	for range 50 {
		n, err := a.worker.RunOnce(context.Background())
		if err != nil {
			a.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	a.t.Fatal("jobs did not settle")
}

type channelView struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Status             string   `json:"status"`
	StatusReason       string   `json:"status_reason"`
	MissingPermissions []string `json:"missing_permissions"`
	RknStatus          string   `json:"rkn_status"`
	CategoryID         string   `json:"category_id"`
}

func (c *client) channels(t *testing.T) []channelView {
	t.Helper()
	r := c.do(http.MethodGet, "/blogger/channels", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list channels: %d %s", r.status, r.body)
	}
	var out struct {
		Items []channelView `json:"items"`
	}
	r.json(t, &out)
	return out.Items
}

func botAddedEvent(chatID, userID int64) string {
	return fmt.Sprintf(`{"update_type":"bot_added","chat_id":%d,"user":{"user_id":%d,"name":"Анна"},"is_channel":true}`, chatID, userID)
}

func codeEvent(chatID int64, mid, code string) string {
	return fmt.Sprintf(`{"update_type":"message_created","message":{"recipient":{"chat_id":%d,"chat_type":"channel"},"body":{"mid":%q,"text":%q}}}`,
		chatID, mid, code)
}

func rightsEvent(chatID int64) string {
	return fmt.Sprintf(`{"update_type":"bot_admin_permissions_changed","chat_id":%d,"user":{"user_id":1},"is_channel":true}`, chatID)
}

func TestChannelConnection(t *testing.T) {
	a := newTestApp(t)
	fake := a.maxFake()
	const chatID = -70000000001
	fake.SetChat(max.Chat{ChatID: chatID, Type: max.ChatTypeChannel, Title: "Рецепты", IsPublic: true,
		Link: "https://max.ru/recipes", ParticipantsCount: 12000})
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"write"}})

	blg := a.bloggerClient("blogger.s12@example.ru")
	var secrets []string

	// S-12 шаг 1: код подключения.
	r := blg.do(http.MethodPost, "/blogger/channels/link-code", nil)
	var issued struct {
		Code        string `json:"code"`
		BotName     string `json:"bot_name"`
		Instruction string `json:"instruction"`
	}
	r.json(t, &issued)
	if r.status != http.StatusCreated || issued.BotName != "@maxim_test_bot" || !strings.Contains(issued.Instruction, issued.Code) {
		t.Fatalf("link code: %d %s", r.status, r.body)
	}
	secrets = append(secrets, issued.Code, testWebhookPath, testWebhookSecret)

	// Шаг 2: бот добавлен — канал unclaimed, в кабинете его ещё нет.
	a.maxEvent(botAddedEvent(chatID, 500100))
	a.maxEvent(botAddedEvent(chatID, 500100)) // повтор доставки другим телом
	a.settle()
	if list := blg.channels(t); len(list) != 0 {
		t.Fatalf("unclaimed channel visible in cabinet: %+v", list)
	}

	// Шаг 3–4: код в канале. Без права delete — pending_rights с перечнем.
	a.maxEvent(codeEvent(chatID, "mid.code.1", issued.Code))
	a.settle()
	list := blg.channels(t)
	if len(list) != 1 || list[0].Status != "pending_rights" || !slices.Equal(list[0].MissingPermissions, []string{"delete"}) {
		t.Fatalf("after code: %+v", list)
	}
	if !slices.Contains(fake.Deleted(), "mid.code.1") {
		t.Fatalf("code message not deleted: %v", fake.Deleted())
	}
	ch := list[0]

	// Тот же код второй раз не срабатывает.
	a.maxEvent(codeEvent(chatID, "mid.code.2", issued.Code))
	a.settle()
	if slices.Contains(fake.Deleted(), "mid.code.2") {
		t.Fatal("consumed code processed again")
	}

	// Права выданы — moderation.
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"write", "delete", "edit"}})
	a.maxEvent(rightsEvent(chatID))
	a.settle()
	if s := blg.channels(t)[0].Status; s != "moderation" {
		t.Fatalf("after rights: %s", s)
	}

	// Шаг 5: категория, описание, номер РКН (12 000 подписчиков).
	r = blg.do(http.MethodPatch, "/blogger/channels/"+ch.ID, map[string]any{
		"description": "Рецепты на каждый день", "category_id": channeltest.CategoryFood.String(), "rkn_number": "4930000001",
	})
	var got channelView
	r.json(t, &got)
	if r.status != http.StatusOK || got.RknStatus != "pending" || got.CategoryID != channeltest.CategoryFood.String() {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+ch.ID, map[string]any{"category_id": "0190a000-0000-7000-8000-000000009999"}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown category: %d %s", r.status, r.body)
	}
	if r := blg.do(http.MethodGet, "/blogger/channels/"+ch.ID, nil); r.status != http.StatusOK {
		t.Fatalf("get: %d %s", r.status, r.body)
	}

	// Чужой блогер канал не видит.
	other := a.bloggerClient("other.blogger@example.ru")
	if r := other.do(http.MethodGet, "/blogger/channels/"+ch.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign get: %d", r.status)
	}

	// S-25: одобренный канал теряет право write — suspended, права вернулись — active.
	if _, err := a.db.Pool.Exec(context.Background(), `UPDATE channel SET rkn_status = 'confirmed' WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	channeltest.Approve(t, a.db.Pool, uuidOf(t, ch.ID))
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"delete"}})
	a.maxEvent(rightsEvent(chatID))
	a.settle()
	if s := blg.channels(t)[0].Status; s != "suspended" {
		t.Fatalf("rights lost: %s", s)
	}
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"write", "delete"}})
	a.maxEvent(rightsEvent(chatID))
	a.settle()
	if s := blg.channels(t)[0].Status; s != "active" {
		t.Fatalf("rights restored: %s", s)
	}
	a.maxEvent(fmt.Sprintf(`{"update_type":"bot_removed","chat_id":%d,"user":{"user_id":1},"is_channel":true}`, chatID))
	a.settle()
	if s := blg.channels(t)[0].Status; s != "disconnected" {
		t.Fatalf("bot removed: %s", s)
	}
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+ch.ID, map[string]any{"description": "x"}); r.status != http.StatusConflict {
		t.Fatalf("patch disconnected: %d %s", r.status, r.body)
	}
	a.maxEvent(botAddedEvent(chatID, 500300))
	a.settle()
	if s := blg.channels(t)[0].Status; s != "active" {
		t.Fatalf("bot re-added: %s", s)
	}

	// Неверные секреты: 401, событие не записано.
	var before int
	_ = a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM inbound_event`).Scan(&before)
	if code := a.webhook("wrong-path", testWebhookSecret, botAddedEvent(-1, 1)); code != http.StatusUnauthorized {
		t.Fatalf("wrong path: %d", code)
	}
	if code := a.webhook(testWebhookPath, "wrong", botAddedEvent(-1, 1)); code != http.StatusUnauthorized {
		t.Fatalf("wrong header: %d", code)
	}
	var after int
	_ = a.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM inbound_event`).Scan(&after)
	if after != before {
		t.Fatal("rejected webhook stored an event")
	}

	a.checkChannelLog(t, secrets)
}

func TestUnclaimedChannelReleased(t *testing.T) {
	a := newTestApp(t)
	fake := a.maxFake()
	fake.SetChat(max.Chat{ChatID: -5, Type: max.ChatTypeChannel, Title: "Без кода", IsPublic: true})
	fake.SetMembership(-5, max.Membership{IsAdmin: true, Permissions: []string{"write", "delete"}})
	a.maxEvent(botAddedEvent(-5, 1))
	a.settle()
	a.clk.Advance(7*24*time.Hour + time.Minute)
	if err := a.mods.channel.ReleaseUnclaimed(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.settle()
	var status string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT status FROM channel WHERE max_chat_id = -5`).Scan(&status); err != nil || status != "disconnected" {
		t.Fatalf("status %q %v", status, err)
	}
	if !slices.Contains(fake.Left(), -5) {
		t.Fatalf("bot did not leave: %v", fake.Left())
	}
}

func TestBloggerChannelsNeedBloggerCabinet(t *testing.T) {
	a := newTestApp(t)
	adv := a.client()
	adv.login("adv.channels@example.ru")
	if r := adv.do(http.MethodPost, "/organizations", advertiserBody()); r.status != http.StatusCreated {
		t.Fatalf("advertiser: %d %s", r.status, r.body)
	}
	if r := adv.do(http.MethodGet, "/blogger/channels", nil); r.status != http.StatusForbidden {
		t.Fatalf("advertiser lists channels: %d", r.status)
	}
	if r := adv.do(http.MethodPost, "/blogger/channels/link-code", nil); r.status != http.StatusForbidden {
		t.Fatalf("advertiser gets link code: %d", r.status)
	}
	blg := a.bloggerClient("csrf.blogger@example.ru")
	blg.csrf = false
	if r := blg.do(http.MethodPost, "/blogger/channels/link-code", nil); r.status != http.StatusForbidden {
		t.Fatalf("link code without CSRF: %d", r.status)
	}
	if r := blg.do(http.MethodGet, "/channel-categories", nil); r.status != http.StatusOK || !strings.Contains(string(r.body), "food") {
		t.Fatalf("categories: %d %s", r.status, r.body)
	}
}

// checkChannelLog — обязательные события этапа (07 §5.3) есть, секреты — нет.
func (a *testApp) checkChannelLog(t *testing.T, secrets []string) {
	t.Helper()
	logs := a.logs.String()
	for _, m := range []string{
		"inbound event accepted", "inbound event processed", "inbound event rejected", "channel bot added",
		"channel linked", "channel status changed", "channel rights refreshed", "channel link code issued",
		"channel saved", "channel bot removed",
	} {
		if !strings.Contains(logs, `"msg":"`+m+`"`) {
			t.Errorf("required log event %q is missing", m)
		}
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("log contains a secret: %q", s)
		}
	}
}

func uuidOf(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (a *testApp) rocketWork() *rocketworkfake.Fake {
	return a.mods.in.rocketwork.(*rocketworkfake.Fake)
}

// activeContractor проводит онбординг блогера в Рокет Ворк до active (S-11): приглашение,
// регистрация исполнителя и событие сервиса — без HTTP, чтобы сценарии других этапов могли
// принимать заявки (P-03 проверяет исполнителя).
func (a *testApp) activeContractor(orgID uuid.UUID) {
	a.t.Helper()
	ctx := context.Background()
	if _, err := a.mods.payout.Invite(ctx, orgID); err != nil {
		a.t.Fatalf("rocketwork invite: %v", err)
	}
	if err := a.mods.payout.EnsureContractor(ctx, orgID); err != nil {
		a.t.Fatalf("rocketwork ensure: %v", err)
	}
	c, err := a.mods.payoutStore.ContractorByOrg(ctx, orgID, false)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := a.rocketWork().ActivateContractor(c.ExternalID); err != nil {
		a.t.Fatal(err)
	}
	if err := a.mods.payout.ApplyContractor(ctx, c.ExternalID); err != nil {
		a.t.Fatalf("rocketwork activate: %v", err)
	}
}
