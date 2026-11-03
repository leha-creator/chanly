package bootstrap

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	notificationdomain "github.com/leha-creator/chanly/internal/notification/domain"
)

type notificationView struct {
	ID         string `json:"id"`
	Template   string `json:"template"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	ObjectType string `json:"object_type"`
	ObjectID   string `json:"object_id"`
	OrderID    string `json:"order_id"`
	Link       string `json:"link"`
	IsRead     bool   `json:"is_read"`
}

type notificationsPage struct {
	Items      []notificationView `json:"items"`
	NextCursor *string            `json:"next_cursor"`
	Total      *int               `json:"total"`
}

func (c *client) notifications(t *testing.T, query string) notificationsPage {
	t.Helper()
	var page notificationsPage
	r := c.do(http.MethodGet, "/notifications"+query, nil)
	if r.status != http.StatusOK {
		t.Fatalf("list notifications: %d %s", r.status, r.body)
	}
	r.json(t, &page)
	return page
}

func TestNotificationsHTTP(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	if r := a.client().do(http.MethodGet, "/notifications", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("without session: %d", r.status)
	}
	adv := a.newAdvertiser("adv-notify-page@example.ru")
	org := adv.orgID(t)
	order := uuid.New()
	for i := range 3 {
		if err := a.mods.notification.Notify(ctx, notificationdomain.Notice{Template: "placement.accepted", OrganizationID: org,
			Role: notificationdomain.RoleAdvertiser, ObjectType: "placement", ObjectID: uuid.NewString(),
			Data: map[string]any{"order_id": order.String(), "order_number": 1000 + i}}); err != nil {
			t.Fatal(err)
		}
		a.clk.Advance(time.Minute)
	}

	first := adv.notifications(t, "?limit=2")
	if len(first.Items) != 2 || first.NextCursor == nil || first.Total == nil || *first.Total != 3 {
		t.Fatalf("first page: %+v", first)
	}
	n := first.Items[0]
	if n.Template != "placement.accepted" || n.OrderID != order.String() || n.Link != "/adv/orders/"+order.String() ||
		n.IsRead || n.Title == "" || n.ObjectType != "placement" {
		t.Fatalf("item: %+v", n)
	}
	if rest := adv.notifications(t, "?limit=2&cursor="+*first.NextCursor); len(rest.Items) != 1 || rest.NextCursor != nil {
		t.Fatalf("second page: %+v", rest)
	}
	if r := adv.do(http.MethodGet, "/notifications?cursor=broken", nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("broken cursor: %d %s", r.status, r.body)
	}

	// Чужое уведомление не найти; своё отмечается, повтор безопасен.
	admin := a.adminClient("admin-notify-page@example.ru", accountdomain.AdminSenior)
	if r := admin.do(http.MethodPost, "/notifications/"+n.ID+"/read", nil); r.status != http.StatusNotFound {
		t.Fatalf("foreign read: %d %s", r.status, r.body)
	}
	for range 2 {
		if r := adv.do(http.MethodPost, "/notifications/"+n.ID+"/read", nil); r.status != http.StatusNoContent {
			t.Fatalf("read: %d %s", r.status, r.body)
		}
	}
	if unread := adv.notifications(t, "?unread=true"); len(unread.Items) != 2 || *unread.Total != 2 {
		t.Fatalf("unread: %+v", unread)
	}
	for range 2 {
		if r := adv.do(http.MethodPost, "/notifications/read-all", nil); r.status != http.StatusNoContent {
			t.Fatalf("read-all: %d %s", r.status, r.body)
		}
	}
	if unread := adv.notifications(t, "?unread=true"); len(unread.Items) != 0 {
		t.Fatalf("unread after read-all: %+v", unread)
	}
	// Администратор без организации видит свои уведомления (R-12).
	if err := a.mods.notification.Notify(ctx, notificationdomain.Notice{Template: "placement.failed",
		Role: notificationdomain.RoleAdmin, ObjectType: "placement", ObjectID: uuid.NewString(),
		Data: map[string]any{"order_number": 1001}}); err != nil {
		t.Fatal(err)
	}
	if page := admin.notifications(t, ""); len(page.Items) != 1 || page.Items[0].Template != "placement.failed" {
		t.Fatalf("admin: %+v", page)
	}
}
