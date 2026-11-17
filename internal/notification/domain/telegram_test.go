package domain

import (
	"github.com/google/uuid"
	"testing"
)

func TestTelegramRequiresPersonalBinding(t *testing.T) {
	owner, member := uuid.New(), uuid.New()
	for _, role := range []Role{RoleAdvertiser, RoleBlogger} {
		rows, err := Plan(Notice{Template: "placement.disputed", Role: role, OrganizationID: uuid.New()}, Audience{
			UserIDs: []uuid.UUID{owner, member}, TelegramUsers: map[uuid.UUID]int64{member: 42},
		}, seqIDs(), now)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, n := range rows {
			if n.Channel == ChannelTelegramBot {
				count++
				if n.UserID != member || n.TelegramUserID == nil || *n.TelegramUserID != 42 || n.MaxUserID != nil {
					t.Fatal("incorrect Telegram recipient")
				}
			}
		}
		if count != 1 {
			t.Fatalf("role %s Telegram count %d", role, count)
		}
	}
}

func TestTelegramSettingsRequireBindingAndStayOptional(t *testing.T) {
	s := DefaultSettings()
	a := SettingsAccess{Roles: []Role{RoleAdvertiser}}
	p := SettingsPatch{Version: s.Version, Delivery: []Preference{{Group: "advertiser.placements", Event: "placement.failed", Channel: "telegram_bot", Enabled: false}}}
	if _, err := s.Apply(p, a); err == nil {
		t.Fatal("unbound Telegram configurable")
	}
	a.TelegramBot = true
	s, err := s.Apply(p, a)
	if err != nil || s.Enabled("advertiser.placements", "placement.failed", "telegram_bot") {
		t.Fatal("optional Telegram preference ignored", err)
	}
}
