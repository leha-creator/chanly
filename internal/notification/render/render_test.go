package render

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
)

func sample() map[string]any {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return map[string]any{
		"order_id": uuid.Must(uuid.NewV7()), "order_number": int64(1001), "channel_title": "Кухня_у_Маши",
		"publish_at": at, "remove_at": at.Add(24 * time.Hour), "published_at": at, "accept_deadline_at": at,
		"claim_deadline_at": at.Add(48 * time.Hour), "publication_url": "https://max.ru/c/1/mid_1", "views": 12345,
		"amount": int64(1150000), "invoice_number": "MX-000101", "number": "А-000001", "period": "2026-10",
		"reason": "Мало подписчиков", "refund_amount": int64(287500), "receipt_url": "https://lknpd.nalog.ru/x",
		domain.DataLink: "/adv/orders/1",
	}
}

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New("https://cabinet.example.ru/")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEveryTemplateRenders(t *testing.T) {
	r := newRenderer(t)
	for _, tpl := range domain.Catalog() {
		for name, data := range map[string]map[string]any{"full": sample(), "empty": {}} {
			n := domain.Notification{Template: tpl.Code, Payload: data}
			title, text, err := r.Cabinet(n)
			if err != nil {
				t.Fatalf("%s/%s cabinet: %v", tpl.Code, name, err)
			}
			if title == "" || text == "" || strings.Contains(title+text, "<no value>") {
				t.Fatalf("%s/%s: %q %q", tpl.Code, name, title, text)
			}
			msg, err := r.MAX(n)
			if err != nil || msg == "" {
				t.Fatalf("%s/%s max: %v", tpl.Code, name, err)
			}
			subject, plain, html, err := r.Email(n)
			if err != nil || subject != title || !strings.Contains(plain, text) || !strings.Contains(html, "<html") {
				t.Fatalf("%s/%s email: %v %q", tpl.Code, name, err, subject)
			}
		}
	}
}

func TestFormatting(t *testing.T) {
	r := newRenderer(t)
	n := domain.Notification{Template: "placement.removed", Payload: sample()}
	title, text, err := r.Cabinet(n)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Пост снят из «Кухня_у_Маши»" {
		t.Fatalf("title %q", title)
	}
	if !strings.Contains(text, "Просмотров: 12 345.") || !strings.Contains(text, "26.10.2026 15:00 МСК") {
		t.Fatalf("text %q", text)
	}
	_, text, _ = r.Cabinet(domain.Notification{Template: "balance.deposited", Payload: sample()})
	if !strings.Contains(text, "11 500 ₽") {
		t.Fatalf("money %q", text)
	}
	d := sample()
	d["resolution"], d["payout_amount"], d["performance_share"] = "proportional", int64(750000), 7500
	_, text, _ = r.Cabinet(domain.Notification{Template: "placement.dispute_resolved", Payload: d})
	if !strings.Contains(text, "принята частично") || !strings.Contains(text, "Выплата блогеру: "+rub(int64(750000))) ||
		!strings.Contains(text, "Возврат рекламодателю: "+rub(int64(287500))) {
		t.Fatalf("dispute resolved %q", text)
	}
	if got := rub(int64(1050)); got != "10,50 ₽" {
		t.Fatalf("rub %q", got)
	}
	if plural(21, "a", "b", "c") != "a" || plural(12, "a", "b", "c") != "c" || plural(3, "a", "b", "c") != "b" {
		t.Fatal("plural")
	}
}

func TestMAXMarkdownEscapingAndLink(t *testing.T) {
	r := newRenderer(t)
	msg, err := r.MAX(domain.Notification{Template: "placement.published", Payload: sample()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg, "**Пост опубликован в «Кухня\\_у\\_Маши»**") {
		t.Fatalf("title not escaped: %q", msg)
	}
	if !strings.Contains(msg, "https://max.ru/c/1/mid_1") || !strings.HasSuffix(msg, "[Открыть в кабинете](https://cabinet.example.ru/adv/orders/1)") {
		t.Fatalf("links: %q", msg)
	}
}

func TestMAXTruncates(t *testing.T) {
	r := newRenderer(t)
	data := sample()
	data["reason"] = strings.Repeat("очень длинная причина ", 400)
	msg, err := r.MAX(domain.Notification{Template: "channel.rejected", Payload: data})
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(msg); n > MaxTextLength {
		t.Fatalf("length %d", n)
	}
	if !strings.Contains(msg, "…\n\n[Открыть в кабинете]") {
		t.Fatalf("tail: %q", msg[len(msg)-200:])
	}
}

func TestEmailEscapesHTML(t *testing.T) {
	r := newRenderer(t)
	data := sample()
	data["reason"] = "<script>alert(1)</script>"
	_, _, html, err := r.Email(domain.Notification{Template: "channel.rejected", Payload: data})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("html not escaped: %s", html)
	}
}
