package bootstrap

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/integration/max"
)

// advertiserClient — пользователь с организацией-рекламодателем и балансом testFunding.
func (a *testApp) advertiserClient(email string) *client {
	a.t.Helper()
	c := a.newAdvertiser(email)
	a.fund(email, testFunding)
	return c
}

// newAdvertiser — рекламодатель с нулевым балансом.
func (a *testApp) newAdvertiser(email string) *client {
	a.t.Helper()
	c := a.client()
	c.login(email)
	if r := c.do(http.MethodPost, "/organizations", advertiserBody()); r.status != http.StatusCreated {
		a.t.Fatalf("create advertiser: %d %s", r.status, r.body)
	}
	return c
}

// adminClient — пользователь с ролью администратора (то же, что делает maxim admin grant, R-2).
func (a *testApp) adminClient(email string, role accountdomain.AdminRole) *client {
	a.t.Helper()
	if _, err := a.mods.account.SetAdminRole(context.Background(), email, &role); err != nil {
		a.t.Fatalf("admin grant: %v", err)
	}
	c := a.client()
	c.login(email)
	return c
}

// moderatedChannel — канал блогера, подключённый событиями MAX, с категорией и ценой:
// состояние moderation. Возвращает id канала.
func (a *testApp) moderatedChannel(blg *client, chatID int64, subscribers int) string {
	a.t.Helper()
	fake := a.maxFake()
	fake.SetChat(max.Chat{ChatID: chatID, Type: max.ChatTypeChannel, Title: "Рецепты", IsPublic: true,
		Link: "https://max.ru/recipes", ParticipantsCount: subscribers})
	fake.SetMembership(chatID, max.Membership{IsAdmin: true, Permissions: []string{"write", "delete"}})
	var issued struct {
		Code string `json:"code"`
	}
	blg.do(http.MethodPost, "/blogger/channels/link-code", nil).json(a.t, &issued)
	a.maxEvent(botAddedEvent(chatID, 500100))
	a.settle()
	a.maxEvent(codeEvent(chatID, "mid.code", issued.Code))
	a.settle()
	var id string
	for _, c := range blg.channels(a.t) {
		if c.Title == "Рецепты" && c.Status == "moderation" {
			id = c.ID
		}
	}
	if id == "" {
		a.t.Fatalf("channel not in moderation: %+v", blg.channels(a.t))
	}
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{
		"description": "Рецепты на каждый день", "category_id": channeltest.CategoryFood.String(),
	}); r.status != http.StatusOK {
		a.t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r := blg.do(http.MethodPut, "/blogger/channels/"+id+"/prices", map[string]any{
		"prices": []map[string]any{{"format_id": "f_1_24", "amount": 300000, "is_enabled": true}},
	}); r.status != http.StatusOK {
		a.t.Fatalf("prices: %d %s", r.status, r.body)
	}
	return id
}

type catalogPage struct {
	Items []struct {
		ID     string `json:"id"`
		Prices []struct {
			FormatID string `json:"format_id"`
			Total    int64  `json:"total_amount"`
		} `json:"prices"`
		CPV *int64 `json:"cpv"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func TestCatalogHTTP(t *testing.T) {
	a := newTestApp(t)
	blg := a.bloggerClient("blogger.catalog@example.ru")
	adv := a.advertiserClient("adv.catalog@example.ru")
	mod := a.adminClient("moderator@example.ru", accountdomain.AdminModerator)

	// Справочник форматов — любому вошедшему.
	var formats struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if r := blg.do(http.MethodGet, "/placement-formats", nil); r.status != http.StatusOK {
		t.Fatalf("formats: %d", r.status)
	} else if r.json(t, &formats); len(formats.Items) != 3 {
		t.Fatalf("formats %+v", formats)
	}

	// Каталог — только кабинету рекламодателя (R-12).
	if r := blg.do(http.MethodGet, "/catalog/channels", nil); r.status != http.StatusForbidden {
		t.Fatalf("catalog for blogger: %d", r.status)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels?sort=bogus", nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown sort: %d", r.status)
	}

	first := a.moderatedChannel(blg, -70000000101, 4000)

	// Администратор: обычный пользователь — 403, модератор одобряет.
	if r := adv.do(http.MethodPost, "/admin/channels/"+first+"/approve", nil); r.status != http.StatusForbidden {
		t.Fatalf("approve by advertiser: %d", r.status)
	} else if _, d := r.errorCode(t); d["reason"] != "admin_required" {
		t.Fatalf("reason %v", d)
	}
	var queue struct {
		Items []struct {
			ID           string `json:"id"`
			Organization struct {
				INN string `json:"inn"`
			} `json:"organization"`
		} `json:"items"`
	}
	if r := mod.do(http.MethodGet, "/admin/channels?status=moderation", nil); r.status != http.StatusOK {
		t.Fatalf("queue: %d %s", r.status, r.body)
	} else if r.json(t, &queue); len(queue.Items) != 1 || queue.Items[0].ID != first || !bytes.Contains([]byte(queue.Items[0].Organization.INN), []byte("*")) {
		t.Fatalf("queue %s", r.body)
	}
	if r := mod.do(http.MethodGet, "/admin/channels/"+first, nil); r.status != http.StatusOK {
		t.Fatalf("admin get: %d %s", r.status, r.body)
	}
	if r := mod.do(http.MethodPost, "/admin/channels/"+first+"/reject", map[string]any{"reason": ""}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("reject without reason: %d", r.status)
	}
	mod.csrf = false
	if r := mod.do(http.MethodPost, "/admin/channels/"+first+"/approve", nil); r.status != http.StatusForbidden {
		t.Fatalf("approve without csrf: %d", r.status)
	}
	mod.csrf = true
	if r := mod.do(http.MethodPost, "/admin/channels/"+first+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	a.ordSettle() // площадка в ОРД: канал виден в каталоге (R-14 этапа «Маркировка»)

	// Канал без цены → 409 с missing.
	second := a.moderatedChannel(a.bloggerClient("blogger2.catalog@example.ru"), -70000000102, 6000)
	if _, err := a.db.Pool.Exec(context.Background(), `DELETE FROM channel_price WHERE channel_id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if r := mod.do(http.MethodPost, "/admin/channels/"+second+"/approve", nil); r.status != http.StatusConflict {
		t.Fatalf("approve without price: %d %s", r.status, r.body)
	} else if _, d := r.errorCode(t); !slices.Equal(toStrings(d["missing"]), []string{"price"}) {
		t.Fatalf("missing %v", d)
	}

	// Каталог: канал виден, цена с комиссией, страницы по курсору.
	var page catalogPage
	r := adv.do(http.MethodGet, "/catalog/channels?limit=1", nil)
	if r.status != http.StatusOK {
		t.Fatalf("catalog: %d %s", r.status, r.body)
	}
	r.json(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != first || page.Items[0].Prices[0].Total != 345000 || page.NextCursor != "" {
		t.Fatalf("catalog page %s", r.body)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels/"+first, nil); r.status != http.StatusOK {
		t.Fatalf("card: %d %s", r.status, r.body)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels/"+second, nil); r.status != http.StatusNotFound {
		t.Fatalf("card of moderated channel: %d", r.status)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels?cursor=broken", nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("broken cursor: %d", r.status)
	}

	// Недоступность и цены в кабинете блогера: CSRF обязателен, 422 с причиной.
	starts := testStart.Add(48 * time.Hour).Format(time.RFC3339)
	ends := testStart.Add(72 * time.Hour).Format(time.RFC3339)
	blg.csrf = false
	if r := blg.do(http.MethodPost, "/blogger/channels/"+first+"/unavailability", map[string]any{"starts_at": starts, "ends_at": ends}); r.status != http.StatusForbidden {
		t.Fatalf("unavailability without csrf: %d", r.status)
	}
	blg.csrf = true
	var ch struct {
		Unavailability []struct {
			ID string `json:"id"`
		} `json:"unavailability"`
		Prices []struct {
			Total int64 `json:"total_amount"`
		} `json:"prices"`
	}
	r = blg.do(http.MethodPost, "/blogger/channels/"+first+"/unavailability", map[string]any{"starts_at": starts, "ends_at": ends})
	if r.status != http.StatusCreated {
		t.Fatalf("unavailability: %d %s", r.status, r.body)
	}
	r.json(t, &ch)
	if len(ch.Unavailability) != 1 || ch.Prices[0].Total != 345000 {
		t.Fatalf("channel %s", r.body)
	}
	if r := blg.do(http.MethodPost, "/blogger/channels/"+first+"/unavailability", map[string]any{"starts_at": starts, "ends_at": ends}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("overlap: %d", r.status)
	} else if _, d := r.errorCode(t); d["reason"] != "overlap" {
		t.Fatalf("overlap reason %v", d)
	}
	if r := blg.do(http.MethodPut, "/blogger/channels/"+first+"/prices", map[string]any{
		"prices": []map[string]any{{"format_id": "f_1_24", "amount": 300000, "is_enabled": false}},
	}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("disable last price of active: %d %s", r.status, r.body)
	} else if _, d := r.errorCode(t); d["reason"] != "active_requires_price" {
		t.Fatalf("reason %v", d)
	}
	if r := blg.do(http.MethodDelete, "/blogger/channels/"+first+"/unavailability/"+ch.Unavailability[0].ID, nil); r.status != http.StatusOK {
		t.Fatalf("delete unavailability: %d %s", r.status, r.body)
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
