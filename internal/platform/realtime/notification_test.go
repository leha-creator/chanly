package realtime

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/httpx"
)

func TestCreationLiveBoundaryAndUserIsolation(t *testing.T) {
	h := NewHub(10)
	h.setHealthy(true)
	user, other, id := uuid.New(), uuid.New(), uuid.New()
	own, _ := h.Subscribe(&httpx.Principal{UserID: user}, false)
	admin, _ := h.Subscribe(&httpx.Principal{UserID: user, AdminRole: "moderator"}, true)
	foreign, _ := h.Subscribe(&httpx.Principal{UserID: other}, false)
	r := Record{CreatedAt: own.startedAt.Add(-time.Second), Scope: "user", AudienceID: &user, Event: Event{Version: 1, Topic: "notification.created", ObjectID: &id}}
	h.Publish(r)
	if len(own.Events) != 0 || len(admin.Events) != 0 {
		t.Fatal("history signalled as live")
	}
	r.CreatedAt = time.Now().Add(time.Millisecond)
	h.Publish(r)
	for _, s := range []*Subscription{own, admin} {
		e := <-s.Events
		if e.Mode != "live" || e.ObjectID == nil || *e.ObjectID != id {
			t.Fatal(e)
		}
	}
	if len(foreign.Events) != 0 {
		t.Fatal("foreign creation leaked")
	}
	r.Event.Topic = "settings.changed"
	r.Event.Mode = ""
	h.Publish(r)
	if len(own.Events) != 1 || len(admin.Events) != 1 || len(foreign.Events) != 0 {
		t.Fatal("settings audience mismatch")
	}
}
