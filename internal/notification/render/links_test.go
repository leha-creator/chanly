package render

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
)

// Exercise the production planner and all delivery renderers for every template/role,
// with expected URLs matching the frontend's detail routes rather than API routes.
func TestCatalogDeliveryLinks(t *testing.T) {
	r := newRenderer(t)
	id := uuid.MustParse("01a1ea9a-cdaa-729c-a382-74249dfa86fd")
	user := uuid.New()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	audience := domain.Audience{UserIDs: []uuid.UUID{user}, MAXUsers: map[uuid.UUID]int64{user: 1}, TelegramUsers: map[uuid.UUID]int64{user: 2}}
	for _, tpl := range domain.Catalog() {
		for _, role := range tpl.Roles {
			t.Run(tpl.Code+"/"+string(role), func(t *testing.T) {
				objectType := strings.SplitN(tpl.Code, ".", 2)[0]
				var want string
				switch role {
				case domain.RoleAdmin:
					want = "/adm/placement/" + id.String()
				case domain.RoleAdvertiser:
					want = "/adv/order/" + id.String()
					if objectType == "balance" || objectType == "closing_acts" {
						want = "/adv/balance"
					}
				case domain.RoleBlogger:
					switch objectType {
					case "channel":
						want = "/blg/channel/" + id.String()
					case "placement":
						want = "/blg/placement/" + id.String()
					default:
						want = "/blg/payouts"
					}
				}
				data := sample()
				delete(data, domain.DataLink)
				delete(data, domain.DataOrderID)
				if objectType == "placement" {
					data[domain.DataOrderID] = id
				}
				data[domain.DataChannelTitle] = "Тестовый канал"
				notice := domain.Notice{Template: tpl.Code, Role: role, ObjectType: objectType, ObjectID: id.String(), Data: data}
				rows, err := domain.Plan(notice, audience, uuid.New, at)
				if err != nil {
					t.Fatal(err)
				}
				wantRows := 4
				if role == domain.RoleAdmin {
					wantRows = 1
				}
				if len(rows) != wantRows {
					t.Fatalf("delivery count %d, want %d", len(rows), wantRows)
				}
				for _, n := range rows {
					if n.Link() != want || n.Payload[domain.DataLink] != want {
						t.Fatalf("%s link %q (stored %v), want %q", n.Channel, n.Link(), n.Payload[domain.DataLink], want)
					}
					title, text, err := r.Cabinet(n)
					if err != nil {
						t.Fatal(err)
					}
					link := "https://cabinet.example.ru" + want
					switch n.Channel {
					case domain.ChannelMaxBot:
						msg, err := r.MAX(n)
						if err != nil || msg != "**"+title+"**\n\n"+text+"\n\n[Открыть в кабинете]("+link+")" {
							t.Fatalf("MAX message %q: %v", msg, err)
						}
					case domain.ChannelTelegramBot:
						msg, err := r.Telegram(n)
						if err != nil || msg != title+"\n\n"+text+"\n\nОткрыть в кабинете: "+link {
							t.Fatalf("Telegram message %q: %v", msg, err)
						}
					case domain.ChannelEmail:
						_, plain, html, err := r.Email(n)
						if err != nil || !strings.Contains(plain, link) || !strings.Contains(html, `href="`+link+`"`) {
							t.Fatalf("email lost link: %v", err)
						}
					}
				}
			})
		}
	}
}

func TestQueuedNotificationUsesCorrectedLink(t *testing.T) {
	r := newRenderer(t)
	n := domain.Notification{Template: "channel.approved", Payload: map[string]any{
		domain.DataLink: "/blg/channels/01a1ea9a-cdaa-729c-a382-74249dfa86fd",
	}}
	const link = "https://cabinet.example.ru/blg/channel/01a1ea9a-cdaa-729c-a382-74249dfa86fd"
	maxText, err := r.MAX(n)
	if err != nil || !strings.HasSuffix(maxText, "[Открыть в кабинете]("+link+")") {
		t.Fatalf("MAX legacy link: %q, %v", maxText, err)
	}
	telegramText, err := r.Telegram(n)
	if err != nil || !strings.HasSuffix(telegramText, "Открыть в кабинете: "+link) {
		t.Fatalf("Telegram legacy link: %q, %v", telegramText, err)
	}
	_, plain, html, err := r.Email(n)
	if err != nil || !strings.Contains(plain, link) || !strings.Contains(html, `href="`+link+`"`) {
		t.Fatalf("email legacy link: %v", err)
	}
}

func TestNotificationInstructions(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range []struct {
		code, want, obsolete string
	}{
		{"channel.disconnected", "повторно подключите канал в кабинете", "код подключения"},
		{"payout.failed", "обратитесь в поддержку", "платформа повторит выплату"},
		{"payout.sent", "принято", "Рекламодатель принял"},
		{"placement.returned", "вернулось в подготовку", "Рекламодатель меняет"},
		{"rocketwork.blocked", "Проверьте статус самозанятого", "tax_status_lost"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			data := sample()
			data["reason"] = "tax_status_lost"
			n := domain.Notification{Template: tc.code, Payload: data}
			for _, render := range []func(domain.Notification) (string, error){r.MAX, r.Telegram} {
				msg, err := render(n)
				if err != nil || !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.obsolete) {
					t.Fatalf("instructions: %q, %v", msg, err)
				}
			}
		})
	}
}
