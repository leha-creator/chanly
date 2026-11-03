package bootstrap

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/integration/max"
)

// TestCatalogEndToEnd — этап «Каталог» на собранных api и worker: S-13 → S-16 → S-02,
// отказ модерации, S-32 с правилом РКН (R-16) и журнал (07 §5).
func TestCatalogEndToEnd(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	const chatID = -70000000201
	blg := a.bloggerClient("blogger.e2e@example.ru")
	adv := a.advertiserClient("adv.e2e@example.ru")
	mod := a.adminClient("moderator.e2e@example.ru", accountdomain.AdminModerator)
	var org struct {
		INN string `json:"inn"`
	}
	blg.do(http.MethodGet, "/organizations/current", nil).json(t, &org)

	// S-13: канал подключён, категория и цена; интервал недоступности.
	id := a.moderatedChannel(blg, chatID, 9999)
	starts, ends := testStart.Add(72*time.Hour), testStart.Add(96*time.Hour)
	if r := blg.do(http.MethodPost, "/blogger/channels/"+id+"/unavailability",
		map[string]any{"starts_at": starts.Format(time.RFC3339), "ends_at": ends.Format(time.RFC3339)}); r.status != http.StatusCreated {
		t.Fatalf("unavailability: %d %s", r.status, r.body)
	}

	// S-16: отказ с причиной → блогер правит описание → канал снова в очереди.
	const rejectReason = "Описание не про еду, уточните тематику"
	if r := mod.do(http.MethodPost, "/admin/channels/"+id+"/reject", map[string]any{"reason": rejectReason}); r.status != http.StatusOK {
		t.Fatalf("reject: %d %s", r.status, r.body)
	}
	if c := blg.channels(t)[0]; c.Status != "rejected" || c.StatusReason != rejectReason {
		t.Fatalf("after reject: %+v", c)
	}
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{"description": "Супы и пироги"}); r.status != http.StatusOK {
		t.Fatalf("edit: %d %s", r.status, r.body)
	}
	var queue struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	mod.do(http.MethodGet, "/admin/channels?status=moderation", nil).json(t, &queue)
	if len(queue.Items) != 1 || queue.Items[0].ID != id {
		t.Fatalf("queue after edit: %+v", queue)
	}
	if r := mod.do(http.MethodPost, "/admin/channels/"+id+"/approve", nil); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	a.ordSettle() // площадка в ОРД: канал виден в каталоге (R-14 этапа «Маркировка»)

	// S-02: рекламодатель находит канал по категории и цене, сортирует по CPV, открывает карточку.
	const secretQuery = "секретныйзапросрекламодателя"
	q := url.Values{"category_id": {channeltest.CategoryFood.String()}, "format_id": {"f_1_24"},
		"price_to": {"400000"}, "sort": {"cpv_asc"}}
	var page catalogPage
	if r := adv.do(http.MethodGet, "/catalog/channels?"+q.Encode(), nil); r.status != http.StatusOK {
		t.Fatalf("catalog: %d %s", r.status, r.body)
	} else if r.json(t, &page); len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatalf("catalog page %s", r.body)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels?q="+url.QueryEscape(secretQuery), nil); r.status != http.StatusOK {
		t.Fatalf("search: %d", r.status)
	}
	var card struct {
		Unavailability []struct {
			StartsAt time.Time `json:"starts_at"`
		} `json:"unavailability"`
		MetricsHistory []struct {
			SubscribersCount int `json:"subscribers_count"`
		} `json:"metrics_history"`
		InCart bool `json:"in_cart"`
	}
	adv.do(http.MethodGet, "/catalog/channels/"+id, nil).json(t, &card)
	if len(card.Unavailability) != 1 || !card.Unavailability[0].StartsAt.Equal(starts) || card.InCart || len(card.MetricsHistory) != 0 {
		t.Fatalf("card %+v", card)
	}

	// Выключить все цены активного канала нельзя; канал остаётся в каталоге.
	if r := blg.do(http.MethodPut, "/blogger/channels/"+id+"/prices", map[string]any{
		"prices": []map[string]any{{"format_id": "f_1_24", "amount": 300000, "is_enabled": false}},
	}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("disable all prices: %d", r.status)
	}

	// S-32: наступают сутки, schedule_channel_metrics и refresh_channel_metrics выполняются.
	fake := a.maxFake()
	views := 1500
	fake.SetMessages(chatID, []max.ChannelMessage{{MID: "m1", At: testStart.Add(-48 * time.Hour), Views: &views}})
	if err := a.worker.EnsurePeriodic(ctx); err != nil {
		t.Fatal(err)
	}
	a.nextDay()
	adv.do(http.MethodGet, "/catalog/channels/"+id, nil).json(t, &card)
	if len(card.MetricsHistory) != 1 || card.MetricsHistory[0].SubscribersCount != 9999 {
		t.Fatalf("snapshot not in card: %+v", card.MetricsHistory)
	}

	// Рост до 10 000 подписчиков → rkn_required, канал пропал из каталога.
	fake.SetChat(max.Chat{ChatID: chatID, Type: max.ChatTypeChannel, Title: "Рецепты", IsPublic: true,
		Link: "https://max.ru/recipes", ParticipantsCount: 10000})
	a.nextDay()
	if c := blg.channels(t)[0]; c.Status != "suspended" || c.StatusReason != "rkn_required" {
		t.Fatalf("after growth: %+v", c)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels/"+id, nil); r.status != http.StatusNotFound {
		t.Fatalf("rkn_required card: %d", r.status)
	}
	// Блогер вводит номер, администратор подтверждает → канал снова в каталоге.
	if r := blg.do(http.MethodPatch, "/blogger/channels/"+id, map[string]any{"rkn_number": "4930000777"}); r.status != http.StatusOK {
		t.Fatalf("rkn number: %d %s", r.status, r.body)
	}
	// Сессия администратора живёт 12 часов (07 §4): за двое суток она истекла.
	if r := mod.do(http.MethodGet, "/admin/channels", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("admin session after 2 days: %d", r.status)
	}
	mod.login("moderator.e2e@example.ru")
	if r := mod.do(http.MethodPost, "/admin/channels/"+id+"/rkn-check", map[string]any{"result": "confirmed"}); r.status != http.StatusOK {
		t.Fatalf("rkn check: %d %s", r.status, r.body)
	}
	if r := adv.do(http.MethodGet, "/catalog/channels/"+id, nil); r.status != http.StatusOK {
		t.Fatalf("card after rkn confirmed: %d", r.status)
	}

	// Журнал: обязательные события есть, персональных данных и текста отказа нет (07 §5.3, §5.4).
	logs := a.logs.String()
	for _, ev := range []string{`"msg":"channel approved"`, `"msg":"channel rejected"`, `"msg":"channel status changed"`,
		`"msg":"channel metrics refreshed"`, `"msg":"channel rkn required"`, `"msg":"channel rkn checked"`,
		`"msg":"channel metrics scheduled"`} {
		if !strings.Contains(logs, ev) {
			t.Errorf("log has no %s", ev)
		}
	}
	for _, secret := range []string{secretQuery, org.INN, "blogger.e2e@example.ru", "adv.e2e@example.ru", rejectReason} {
		if secret != "" && strings.Contains(logs, secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}

// nextDay сдвигает часы на 36 часов и выполняет готовые задания: суточные метрики
// ставятся в 00:05 по Москве и распределены по всем суткам (R-13), поэтому от полудня
// нужно пройти до конца следующих суток.
func (a *testApp) nextDay() {
	a.t.Helper()
	for range 6 {
		a.clk.Advance(6 * time.Hour)
		a.settle()
	}
}
