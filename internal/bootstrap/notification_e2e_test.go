package bootstrap

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Сквозной сценарий этапа «Уведомления и отчёт по кампании»: события заказа и размещения доходят
// до кабинета, бота MAX и почты со ссылкой в кабинет; отчёт по заказу и CSV показывают просмотры,
// ссылку на пост и UTM-метку.
func TestNotificationsEndToEnd(t *testing.T) {
	a := newTestApp(t)
	const advEmail, blgEmail = "adv.notify.e2e@example.ru", "blg.notify.e2e@example.ru"
	adv := a.advertiserClient(advEmail)
	blg := a.bloggerClient(blgEmail)
	bindMAXBot(t, a, adv, 8)
	bindMAXBot(t, a, blg, 7)
	_, pids := a.autopostOrder(adv, blg, "Уведомления")
	pid := pids[0]
	a.settle()

	// Рекламодатель: пост одобрен, блогер принял. Блогер: запрос в кабинете, боте и почте (R-2).
	advPage := adv.notifications(t, "")
	if !hasTemplate(advPage, "order.post_approved") || !hasTemplate(advPage, "placement.accepted") {
		t.Fatalf("advertiser notifications: %+v", advPage.Items)
	}
	blgPage := blg.notifications(t, "")
	if !hasTemplate(blgPage, "placement.request") {
		t.Fatalf("blogger notifications: %+v", blgPage.Items)
	}
	link := devCabinetBaseURL + "/blg/placements?placement=" + pid
	var botMsg bool
	for _, m := range a.maxFake().Sent() {
		if m.UserID == 7 && strings.Contains(m.Text, link) {
			botMsg = true
		}
	}
	if !botMsg {
		t.Fatalf("no bot message with %s: %+v", link, a.maxFake().Sent())
	}
	if !mailedWith(a, blgEmail, link) {
		t.Fatal("no blogger email with the cabinet link")
	}

	// Публикация «сейчас» — письмо рекламодателю со ссылкой на заказ.
	a.clk.Set(at(11, 30, 0))
	a.schedule(blg, pid, map[string]any{"mode": "now"})
	a.clk.Set(at(11, 30, 4))
	a.settle()
	if st, _ := a.placementState(pid); st != "published" {
		t.Fatalf("published: %s", st)
	}
	var orderID string
	for _, n := range adv.notifications(t, "").Items {
		if n.Template == "placement.published" {
			orderID = n.OrderID
		}
	}
	if orderID == "" || !mailedWith(a, advEmail, devCabinetBaseURL+"/adv/orders/"+orderID) {
		t.Fatalf("no published email for order %q", orderID)
	}

	var advertiserBotMessage bool
	for _, m := range a.maxFake().Sent() {
		if m.UserID == 8 && strings.Contains(m.Text, devCabinetBaseURL+"/adv/orders/"+orderID) {
			advertiserBotMessage = true
		}
	}
	if !advertiserBotMessage {
		t.Fatal("no advertiser bot notification with order link")
	}

	// Снятие по сроку — просмотры в уведомлении и отчёте.
	post := a.maxFake().Posts()[0]
	a.maxFake().SetViews(post.ID, 1500)
	a.clk.Set(at(11, 30, 4).Add(24*time.Hour + 10*time.Second))
	a.settle()
	if st, _ := a.placementState(pid); st != "removed" {
		t.Fatalf("removed: %s", st)
	}
	var removedText string
	for _, n := range adv.notifications(t, "").Items {
		if n.Template == "placement.removed" {
			removedText = n.Text
		}
	}
	if !strings.Contains(removedText, "Просмотров: 1\u00a0500") {
		t.Fatalf("removed notice without views: %q", removedText)
	}

	var report struct {
		OrderNumber int64    `json:"order_number"`
		Links       []string `json:"links"`
		UTM         struct {
			Source, Medium, Campaign string
		} `json:"utm"`
		Totals struct {
			Views      int   `json:"views"`
			HeldAmount int64 `json:"held_amount"`
		} `json:"totals"`
		Rows []struct {
			Views          *int   `json:"views"`
			PublicationURL string `json:"publication_url"`
			UTMCampaign    string `json:"utm_campaign"`
		} `json:"rows"`
	}
	r := adv.do(http.MethodGet, "/orders/"+orderID+"/report", nil)
	if r.status != http.StatusOK {
		t.Fatalf("report: %d %s", r.status, r.body)
	}
	r.json(t, &report)
	campaign := "order_" + strconv.FormatInt(report.OrderNumber, 10)
	if len(report.Rows) != 1 || report.Rows[0].Views == nil || *report.Rows[0].Views != 1500 || report.Rows[0].PublicationURL == "" ||
		report.Totals.Views != 1500 || report.Totals.HeldAmount == 0 || report.UTM.Campaign != campaign || report.UTM.Source != "max" ||
		len(report.Links) != 1 || !strings.Contains(report.Links[0], "utm_campaign="+campaign) {
		t.Fatalf("report: %s", r.body)
	}
	csv := adv.do(http.MethodGet, "/orders/"+orderID+"/report.csv", nil)
	if csv.status != http.StatusOK || !strings.HasPrefix(csv.header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(csv.header.Get("Content-Disposition"), "report.csv") ||
		!bytes.Contains(csv.body, []byte(";1500;")) || !bytes.Contains(csv.body, []byte(campaign)) {
		t.Fatalf("csv: %d %v %s", csv.status, csv.header, csv.body)
	}
	if r := blg.do(http.MethodGet, "/orders/"+orderID+"/report", nil); r.status == http.StatusOK {
		t.Fatal("blogger reads the advertiser report")
	}

	// Всё прочитано; адреса почты в журнале не встречаются (07 §5.4).
	if r := adv.do(http.MethodPost, "/notifications/read-all", nil); r.status != http.StatusNoContent {
		t.Fatalf("read-all: %d", r.status)
	}
	if left := adv.notifications(t, "?unread=true"); len(left.Items) != 0 {
		t.Fatalf("unread: %+v", left.Items)
	}
	logs := a.logs.String()
	for _, s := range []string{advEmail, blgEmail} {
		if strings.Contains(logs, s) {
			t.Errorf("log contains %s", s)
		}
	}
	if !strings.Contains(logs, "notification sent") {
		t.Error("no notification sent log")
	}
}

func hasTemplate(p notificationsPage, template string) bool {
	for _, n := range p.Items {
		if n.Template == template {
			return true
		}
	}
	return false
}

func mailedWith(a *testApp, to, text string) bool {
	for _, m := range a.mail.Notifications() {
		if m.To == to && strings.Contains(m.Text, text) && strings.Contains(m.HTML, text) {
			return true
		}
	}
	return false
}
