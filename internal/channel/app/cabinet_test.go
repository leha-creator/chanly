package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestCreateLinkCode(t *testing.T) {
	e := newEnv(t)
	org := e.org()
	first, err := e.svc.CreateLinkCode(e.ctx(), org)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := domain.NormalizeLinkCode(first.Code); !ok || !first.ExpiresAt.Equal(start.Add(time.Hour)) ||
		first.BotName != "@maxim_test_bot" || !strings.Contains(first.Instruction, first.Code) {
		t.Fatalf("issued %+v", first)
	}
	second, err := e.svc.CreateLinkCode(e.ctx(), org)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM auth_code WHERE purpose = 'channel_link' AND consumed_at IS NULL`); n != 1 {
		t.Fatalf("live codes = %d, want 1 (new code consumes previous)", n)
	}
	if first.Code == second.Code {
		t.Fatal("same code issued twice")
	}
	for range 8 {
		if _, err := e.svc.CreateLinkCode(e.ctx(), org); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.CreateLinkCode(e.ctx(), org); !apperr.Is(err, apperr.CodeRateLimited) {
		t.Fatalf("11th code: %v", err)
	}
	e.clk.Advance(61 * time.Minute)
	if _, err := e.svc.CreateLinkCode(e.ctx(), org); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	// Код не попадает ни в журнал приложения, ни в журнал действий (07 §5.4).
	if strings.Contains(e.logs.String(), first.Code) {
		t.Fatal("link code written to the log")
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE data::text LIKE '%' || $1 || '%'`, first.Code); n != 0 {
		t.Fatal("link code written to audit_log")
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.link_code_created' AND actor_type = 'blogger'`); n != 11 {
		t.Fatalf("audit entries = %d", n)
	}
}

// linkedChannel — канал, подключённый к организации через события и задания.
func (e *env) linkedChannel(chatID int64, m domain.Membership) (*domain.Channel, string) {
	e.t.Helper()
	org := e.org()
	issued, err := e.svc.CreateLinkCode(e.ctx(), org)
	if err != nil {
		e.t.Fatal(err)
	}
	e.max.set(chatID, publicCh, m)
	e.event(botAdded(chatID, 500100))
	e.drain()
	e.event(channelPost(chatID, "mid.code", issued.Code))
	e.drain()
	c := e.channel(chatID)
	if c.OrganizationID == nil || *c.OrganizationID != org {
		e.t.Fatalf("channel not linked: %+v", c)
	}
	return c, issued.Code
}

func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.linkedChannel(-100, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	if c.Status != domain.StatusPendingRights {
		t.Fatalf("status %s", c.Status)
	}
	org := *c.OrganizationID
	desc, num := "Рецепты на каждый день", "4930000001"
	cat := channeltest.CategoryFood

	// Без права delete канал остаётся pending_rights.
	got, err := e.svc.Update(e.ctx(), org, c.ID, domain.ProfileUpdate{Description: &desc, CategoryID: &cat})
	if err != nil || got.Status != domain.StatusPendingRights || got.Description != desc {
		t.Fatalf("update: %+v %v", got, err)
	}
	// Права выданы, затем правка уводит канал на модерацию.
	e.max.set(-100, publicCh, allRights)
	e.event(rightsChanged(-100))
	e.drain()
	if s := e.channel(-100).Status; s != domain.StatusModeration {
		t.Fatalf("after rights: %s", s)
	}
	if _, err := e.d.Pool.Exec(ctx, `UPDATE channel SET subscribers_count = 12000 WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.Update(e.ctx(), org, c.ID, domain.ProfileUpdate{RknNumber: &num})
	if err != nil || got.RknStatus != domain.RknPending || got.RknNumber != num {
		t.Fatalf("rkn: %+v %v", got, err)
	}

	unknown := ids.New()
	if _, err := e.svc.Update(e.ctx(), org, c.ID, domain.ProfileUpdate{CategoryID: &unknown}); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("unknown category: %v", err)
	}
	if _, err := e.svc.Update(e.ctx(), e.org(), c.ID, domain.ProfileUpdate{Description: &desc}); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign channel: %v", err)
	}
	if _, err := e.svc.Get(e.ctx(), e.org(), c.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
	if list, err := e.svc.List(e.ctx(), org); err != nil || len(list) != 1 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'channel.updated' AND object_id = $1`, c.ID.String()); n != 2 {
		t.Fatalf("channel.updated entries = %d", n)
	}

	e.event(botRemoved(-100))
	e.drain()
	if _, err := e.svc.Update(e.ctx(), org, c.ID, domain.ProfileUpdate{Description: &desc}); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("disconnected channel update: %v", err)
	}
}

func TestListCategories(t *testing.T) {
	e := newEnv(t)
	list, err := e.svc.ListCategories(e.ctx())
	if err != nil || len(list) != 8 {
		t.Fatalf("categories %d %v", len(list), err)
	}
}
