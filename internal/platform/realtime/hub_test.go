package realtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

func TestAudience(t *testing.T) {
	org1, org2, user1, user2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	adv := &httpx.Principal{UserID: user1, OrganizationID: &org1, Cabinets: []string{"advertiser"}}
	other := &httpx.Principal{UserID: user2, OrganizationID: &org2, Cabinets: []string{"blogger"}}
	moderator := &httpx.Principal{UserID: user1, AdminRole: "moderator"}
	senior := &httpx.Principal{UserID: user2, AdminRole: "senior"}
	cases := []struct {
		name, scope, topic string
		aud                *uuid.UUID
		p                  *httpx.Principal
		admin, want        bool
	}{
		{"own organization", "organization", "post.changed", &org1, adv, false, true},
		{"other organization", "organization", "post.changed", &org1, other, false, false},
		{"own notification", "user", "notification.changed", &user1, adv, false, true},
		{"other user notification", "user", "notification.changed", &user1, other, false, false},
		{"same org other user notification", "user", "notification.changed", &user1, &httpx.Principal{UserID: user2, OrganizationID: &org1}, false, false},
		{"admin no organization", "admin", "admin.order.changed", nil, moderator, true, true},
		{"admin on ordinary stream", "admin", "admin.order.changed", nil, senior, false, false},
		{"admin topic disguised as user", "user", "admin.audit.changed", &user1, adv, false, false},
		{"no admin role", "admin", "admin.order.changed", nil, adv, true, false},
		{"finance moderator", "senior", "admin.finance.changed", nil, moderator, true, false},
		{"finance mislabeled audience", "admin", "admin.finance.changed", nil, moderator, true, false},
		{"finance senior", "senior", "admin.finance.changed", nil, senior, true, true},
		{"catalog advertiser", "catalog", "channel.changed", nil, adv, false, true},
		{"catalog blogger", "catalog", "channel.changed", nil, other, false, false},
		{"admin personal notification", "user", "notification.changed", &user1, moderator, true, true},
		{"other admin notification", "user", "notification.changed", &user2, moderator, true, false},
		{"admin organization stream excluded", "organization", "post.changed", &org1, adv, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Record{Scope: tc.scope, AudienceID: tc.aud, Event: Event{Version: 1, Topic: tc.topic}}
			if got := r.allowed(tc.p, tc.admin); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestHubOverflowAndShutdown(t *testing.T) {
	h := NewHub(1)
	p := &httpx.Principal{UserID: uuid.New()}
	if _, err := h.Subscribe(p, false); err != ErrUnavailable {
		t.Fatal("unhealthy hub accepted subscriber")
	}
	h.setHealthy(true)
	slow, _ := h.Subscribe(p, false)
	fast, _ := h.Subscribe(p, false)
	r := Record{Scope: "user", AudienceID: &p.UserID, Event: Event{Version: 1, Topic: "notification.changed"}}
	h.Publish(r)
	<-fast.Events
	h.Publish(r)
	select {
	case <-slow.Done:
	default:
		t.Fatal("slow subscriber not closed on overflow")
	}
	select {
	case <-fast.Done:
		t.Fatal("fast subscriber affected")
	default:
	}
	<-fast.Events
	h.setHealthy(false)
	select {
	case <-fast.Done:
	default:
		t.Fatal("failure did not close stream")
	}
	if _, err := h.Subscribe(p, false); err == nil {
		t.Fatal("subscribe during recovery succeeded")
	}
	h.setHealthy(true)
	s, _ := h.Subscribe(p, false)
	h.Close()
	s.Close()
	h.setHealthy(true)
	if _, err := h.Subscribe(p, false); err == nil {
		t.Fatal("closed hub reopened")
	}
	select {
	case <-s.Done:
	case <-time.After(time.Second):
		t.Fatal("shutdown")
	}
}
