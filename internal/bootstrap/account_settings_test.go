package bootstrap

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/apigen"
	nd "github.com/leha-creator/chanly/internal/notification/domain"
)

func accountSettings(t *testing.T, c *client) apigen.AccountSettings {
	t.Helper()
	r := c.do("GET", "/me/settings", nil)
	if r.status != 200 {
		t.Fatalf("settings %d %s", r.status, r.body)
	}
	var s apigen.AccountSettings
	r.json(t, &s)
	return s
}
func TestAccountSettingsPersistenceConflictAccess(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("settings-a@example.test")
	other := a.newAdvertiser("settings-b@example.test")
	s := accountSettings(t, c)
	if s.Version != 0 || s.HideNotificationPreview || len(s.Catalog) == 0 {
		t.Fatal("defaults")
	}
	r := c.do("PATCH", "/me/settings", map[string]any{"version": 0, "hide_notification_preview": true})
	if r.status != 200 {
		t.Fatalf("patch %d %s", r.status, r.body)
	}
	if accountSettings(t, other).HideNotificationPreview {
		t.Fatal("settings crossed users")
	}
	r = c.do("PATCH", "/me/settings", map[string]any{"version": 0, "hide_notification_preview": false})
	if r.status != 409 {
		t.Fatal(r.status)
	}
	r = c.do("PATCH", "/me/settings", map[string]any{"version": 1, "delivery": []map[string]any{{"group": "blogger.requests", "event": "*", "channel": "email", "enabled": false}}})
	if r.status != 403 {
		t.Fatal(r.status)
	}
	again := a.client()
	again.login("settings-a@example.test")
	if !accountSettings(t, again).HideNotificationPreview {
		t.Fatal("not persistent")
	}
	// Two clients save the same revision: exactly one succeeds.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, who := range []*client{c, again} {
		wg.Go(func() {
			statuses <- who.do("PATCH", "/me/settings", map[string]any{"version": 1, "hide_notification_preview": false}).status
		})
	}
	wg.Wait()
	close(statuses)
	got := map[int]int{}
	for s := range statuses {
		got[s]++
	}
	if got[200] != 1 || got[409] != 1 {
		t.Fatal(got)
	}
	c.csrf = false
	if r = c.do("PATCH", "/me/settings", map[string]any{"version": 2}); r.status != 403 {
		t.Fatal("missing CSRF accepted")
	}
}
func TestAccountSettingsDeliveryAndQuietHours(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("settings-delivery@example.test")
	org := c.orgID(t)
	patch := func(version int, fields map[string]any) {
		fields["version"] = version
		r := c.do("PATCH", "/me/settings", fields)
		if r.status != 200 {
			t.Fatalf("patch: %d %s", r.status, r.body)
		}
	}
	patch(0, map[string]any{"delivery": []map[string]any{{"group": "advertiser.placements", "event": "*", "channel": "email", "enabled": false}}})
	notify := func(code string) {
		t.Helper()
		if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleAdvertiser, OrganizationID: org, Template: code, ObjectType: "placement", ObjectID: uuid.NewString(), Data: map[string]any{"order_id": uuid.NewString(), "order_number": 1001}}); err != nil {
			t.Fatal(err)
		}
	}
	notify("placement.accepted")
	a.settle()
	if len(a.mail.Notifications()) != 0 || len(c.notifications(t, "").Items) != 1 {
		t.Fatal("delivery settings ignored or history lost")
	}
	patch(1, map[string]any{"delivery": []map[string]any{{"group": "advertiser.placements", "event": "*", "channel": "email", "enabled": true}}, "quiet_hours": map[string]any{"enabled": true, "start": "11:00", "end": "13:00", "timezone": "Europe/Moscow"}})
	// testStart is 12:00 MSK.
	notify("placement.accepted")
	a.settle()
	if len(a.mail.Notifications()) != 0 {
		t.Fatal("quiet hours ignored")
	}
	notify("placement.disputed")
	a.settle()
	if len(a.mail.Notifications()) != 1 {
		t.Fatal("mandatory delivery deferred")
	}
	a.clk.Advance(time.Hour)
	a.settle()
	if len(a.mail.Notifications()) != 2 {
		t.Fatal("deferred mail lost")
	}
}
func TestAccountSettingsSessionsAndEmailChange(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("email-old@example.test")
	other := a.client()
	other.login("email-old@example.test")
	foreign := a.client()
	foreign.login("email-foreign@example.test")
	stream := other.stream(t, "/events")
	var sessions apigen.AccountSessions
	c.do("GET", "/me/sessions", nil).json(t, &sessions)
	if len(sessions.Items) != 2 {
		t.Fatal("sessions missing")
	}
	for _, s := range sessions.Items {
		if s.Current {
			if r := c.do("DELETE", "/me/sessions/"+s.Id.String(), nil); r.status != 422 {
				t.Fatal("current revoked")
			}
		}
	}
	var challenge apigen.EmailChange
	r := c.do("POST", "/me/email-change", map[string]any{"new_email": "email-new@example.test"})
	if r.status != 202 {
		t.Fatalf("start %d %s", r.status, r.body)
	}
	r.json(t, &challenge)
	a.settle()
	old, _ := a.mail.Last("email-old@example.test")
	next, _ := a.mail.Last("email-new@example.test")
	confirm := func(who *client, x, y string) response {
		return who.do("POST", "/me/email-change/"+challenge.Id.String()+"/confirm", map[string]any{"current_code": x, "new_code": y})
	}
	if r = confirm(foreign, old.Code, next.Code); r.status != 404 {
		t.Fatal("foreign challenge accessible")
	}
	if r = confirm(c, next.Code, old.Code); r.status != 422 {
		t.Fatal("purpose not bound")
	}
	if r = c.do("POST", "/me/email-change/"+challenge.Id.String()+"/resend", nil); r.status != 429 || r.header.Get("Retry-After") == "" {
		t.Fatal("resend unbounded")
	}
	if r = confirm(c, old.Code, next.Code); r.status != 204 {
		t.Fatalf("confirm %d %s", r.status, r.body)
	}
	if r = c.do("GET", "/me", nil); r.status != 200 || !strings.Contains(string(r.body), "email-new@example.test") {
		t.Fatal("me stale")
	}
	if r = other.do("GET", "/me", nil); r.status != 401 {
		t.Fatal("other session remains valid")
	}
	stream.closed(t)
	if r = confirm(c, old.Code, next.Code); r.status != 422 {
		t.Fatal("challenge reused")
	}
	for _, secret := range []string{old.Code, next.Code} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatal("code logged")
		}
	}
}
func TestAccountSettingsEmailAttemptsExpiryAndUniqueness(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("limits-old@example.test")
	taken := a.client()
	taken.login("limits-taken@example.test")
	if r := c.do("POST", "/me/email-change", map[string]string{"new_email": "LIMITS-TAKEN@example.test"}); r.status != 409 {
		t.Fatal("duplicate accepted")
	}
	var ch apigen.EmailChange
	r := c.do("POST", "/me/email-change", map[string]string{"new_email": "limits-new@example.test"})
	r.json(t, &ch)
	a.settle()
	old, _ := a.mail.Last("limits-old@example.test")
	next, _ := a.mail.Last("limits-new@example.test")
	wrong := "000000"
	if old.Code == wrong {
		wrong = "000001"
	}
	for range 5 {
		if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": wrong, "new_code": next.Code}); r.status != 422 {
			t.Fatalf("attempt %d", r.status)
		}
	}
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": old.Code, "new_code": next.Code}); r.status != 429 {
		t.Fatal("attempt limit bypassed")
	}
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/resend", nil); r.status != 429 {
		t.Fatal("attempt reset on resend")
	}
	a.clk.Advance(10 * time.Minute)
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": old.Code, "new_code": next.Code}); r.status != 422 {
		t.Fatal("expired challenge accepted")
	}
}
func TestAccountSettingsSSECreationAndSettingsIsolation(t *testing.T) {
	a := newTestApp(t)
	c := a.newAdvertiser("settings-sse@example.test")
	other := a.newAdvertiser("settings-sse-other@example.test")
	s := c.stream(t, "/events")
	foreign := other.stream(t, "/events")
	// Let login/profile invalidations drain, then observe only our two new topics.
	time.Sleep(300 * time.Millisecond)
	r := c.do("PATCH", "/me/settings", map[string]any{"version": 0, "hide_notification_preview": true})
	if r.status != 200 {
		t.Fatal(r.status)
	}
	waitTopic := func(topic string) map[string]any {
		t.Helper()
		for {
			f := s.next(t, "invalidate", 3*time.Second)
			var e map[string]any
			json.Unmarshal(f.Data, &e)
			if e["topic"] == topic {
				return e
			}
		}
	}
	waitTopic("settings.changed")
	if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleAdvertiser, OrganizationID: c.orgID(t), Template: "placement.accepted", ObjectType: "placement", ObjectID: uuid.NewString(), Data: map[string]any{"order_id": uuid.NewString(), "order_number": 1001}}); err != nil {
		t.Fatal(err)
	}
	e := waitTopic("notification.created")
	if e["mode"] != "live" {
		t.Fatal(e)
	}
	id := e["object_id"].(string)
	if r = c.do("GET", "/notifications/"+id, nil); r.status != 200 {
		t.Fatal(r.status)
	}
	if r = other.do("GET", "/notifications/"+id, nil); r.status != 404 {
		t.Fatal("foreign notification leaked")
	}
	c.do("POST", "/notifications/"+id+"/read", nil)
	time.Sleep(350 * time.Millisecond)
	for len(s.Frames) > 0 {
		f := <-s.Frames
		if strings.Contains(string(f.Data), "notification.created") {
			t.Fatal("read replayed creation")
		}
	}
	for len(foreign.Frames) > 0 {
		f := <-foreign.Frames
		if strings.Contains(string(f.Data), "settings.changed") || strings.Contains(string(f.Data), "notification.") {
			t.Fatal("user event leaked")
		}
	}
	reconnected := c.stream(t, "/events")
	time.Sleep(350 * time.Millisecond)
	for len(reconnected.Frames) > 0 {
		f := <-reconnected.Frames
		if strings.Contains(string(f.Data), "notification.created") {
			t.Fatal("reconnect replayed creation")
		}
	}
}

func TestAccountSettingsExplicitSessionRevocation(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("session-owner@example.test")
	second := a.client()
	second.login("session-owner@example.test")
	third := a.client()
	third.login("session-owner@example.test")
	foreign := a.client()
	foreign.login("session-foreign@example.test")
	var foreignSessions apigen.AccountSessions
	foreign.do("GET", "/me/sessions", nil).json(t, &foreignSessions)
	if r := c.do("DELETE", "/me/sessions/"+foreignSessions.Items[0].Id.String(), nil); r.status != 404 {
		t.Fatal("foreign session revoked")
	}
	var sessions apigen.AccountSessions
	c.do("GET", "/me/sessions", nil).json(t, &sessions)
	var target uuid.UUID
	for _, s := range sessions.Items {
		if !s.Current {
			target = s.Id
			break
		}
	}
	if r := c.do("DELETE", "/me/sessions/"+target.String(), nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := c.do("DELETE", "/me/sessions/others", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	for _, client := range []*client{second, third} {
		if r := client.do("GET", "/me", nil); r.status != 401 {
			t.Fatal("revoked session authorized")
		}
	}
	if r := c.do("GET", "/me", nil); r.status != 200 {
		t.Fatal("current lost")
	}
	if r := foreign.do("GET", "/me", nil); r.status != 200 {
		t.Fatal("foreign affected")
	}
	if r := c.do("POST", "/auth/logout", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := c.do("GET", "/me", nil); r.status != 401 {
		t.Fatal("logout ineffective")
	}
}

func TestAccountSettingsEmailResendReplacementAndStartLimit(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("resend-old@example.test")
	var ch apigen.EmailChange
	r := c.do("POST", "/me/email-change", map[string]string{"new_email": "RESEND-NEW@example.test"})
	if r.status != 202 {
		t.Fatalf("start %d %s", r.status, r.body)
	}
	r.json(t, &ch)
	a.settle()
	old, _ := a.mail.Last("resend-old@example.test")
	next, _ := a.mail.Last("resend-new@example.test")
	a.clk.Advance(time.Minute)
	r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/resend", nil)
	if r.status != 202 {
		t.Fatalf("resend %d %s", r.status, r.body)
	}
	var resent apigen.EmailChange
	r.json(t, &resent)
	if !ch.ExpiresAt.Equal(resent.ExpiresAt) {
		t.Fatal("resend extends expiration")
	}
	a.settle()
	r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": old.Code, "new_code": next.Code})
	if r.status != 422 {
		t.Fatal("old pair accepted")
	}
	a.clk.Advance(time.Minute)
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/resend", nil); r.status != 202 {
		t.Fatal(r.status)
	}
	a.clk.Advance(time.Minute)
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/resend", nil); r.status != 429 {
		t.Fatal("unbounded resend")
	}
	for range 2 {
		if r = c.do("POST", "/me/email-change", map[string]string{"new_email": "resend-another@example.test"}); r.status != 202 {
			t.Fatal(r.status)
		}
	}
	if r = c.do("POST", "/me/email-change", map[string]string{"new_email": "resend-third@example.test"}); r.status != 429 {
		t.Fatal("unbounded starts")
	}
	if r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": old.Code, "new_code": next.Code}); r.status != 422 {
		t.Fatal("superseded challenge accepted")
	}
}

func TestAccountSettingsMAXDeliveryAndUnavailableChannel(t *testing.T) {
	a := newTestApp(t)
	blg, org := a.newBlogger("max-settings@example.test")
	for _, o := range accountSettings(t, blg).Catalog {
		if o.Channel == "max_bot" && o.Available {
			t.Fatal("MAX offered without channel")
		}
	}
	r := blg.do("PATCH", "/me/settings", map[string]any{"version": 0, "delivery": []map[string]any{{"group": "blogger.requests", "event": "*", "channel": "max_bot", "enabled": false}}})
	if r.status != 403 {
		t.Fatal("unavailable MAX accepted")
	}
	ch := a.activeChannel(blg, "Настройки MAX", 1_000_000)
	a.settle()
	before := len(a.maxFake().Sent())
	r = blg.do("PATCH", "/me/settings", map[string]any{"version": 0, "delivery": []map[string]any{{"group": "blogger.requests", "event": "*", "channel": "max_bot", "enabled": false}}})
	if r.status != 200 {
		t.Fatalf("disable MAX %d %s", r.status, r.body)
	}
	if err := a.mods.notification.Notify(t.Context(), nd.Notice{Role: nd.RoleBlogger, OrganizationID: org, Template: "placement.request", ObjectType: "placement", ObjectID: uuid.NewString(), Data: map[string]any{"channel_id": ch, "order_number": 1001}}); err != nil {
		t.Fatal(err)
	}
	a.settle()
	if len(a.maxFake().Sent()) != before {
		t.Fatal("MAX setting ignored")
	}
	if !hasTemplate(blg.notifications(t, ""), "placement.request") {
		t.Fatal("cabinet event suppressed")
	}
}

func TestAccountSettingsEmailUniqueRaceIsAtomic(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("atomic-old@example.test")
	a.clk.Advance(123 * time.Nanosecond)
	var ch apigen.EmailChange
	r := c.do("POST", "/me/email-change", map[string]string{"new_email": "atomic-new@example.test"})
	r.json(t, &ch)
	a.settle()
	old, _ := a.mail.Last("atomic-old@example.test")
	next, _ := a.mail.Last("atomic-new@example.test")
	if old.Purpose != "current" || next.Purpose != "new" {
		t.Fatal("email change codes not delivered with sub-microsecond clock")
	}
	concurrent := a.client()
	concurrent.login("atomic-new@example.test")
	r = c.do("POST", "/me/email-change/"+ch.Id.String()+"/confirm", map[string]string{"current_code": old.Code, "new_code": next.Code})
	if r.status != 409 || !strings.Contains(string(r.body), "email_taken") {
		t.Fatalf("unique race %d %s", r.status, r.body)
	}
	r = c.do("GET", "/me", nil)
	if !strings.Contains(string(r.body), "atomic-old@example.test") {
		t.Fatal("partial email update")
	}
	if r = concurrent.do("GET", "/me", nil); r.status != 200 {
		t.Fatal("unrelated account revoked")
	}
}
