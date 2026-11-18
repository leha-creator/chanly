package realtime

import (
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"testing"
)

func TestSupportAudiences(t *testing.T) {
	id, other := ids.New(), ids.New()
	record := Record{Scope: "user", AudienceID: &id, Event: Event{Version: 1, Topic: "admin.support.read"}}
	p := &httpx.Principal{UserID: id, AdminRole: "moderator"}
	if !record.allowed(p, true) || record.allowed(p, false) {
		t.Fatal("support read audience escaped admin stream")
	}
	p.UserID = other
	if record.allowed(p, true) {
		t.Fatal("other operator received personal marker")
	}
	record = Record{Scope: "admin", Event: Event{Version: 1, Topic: "admin.support.changed"}}
	if !record.allowed(p, true) || record.allowed(p, false) {
		t.Fatal("support broadcast escaped admin stream")
	}
}
