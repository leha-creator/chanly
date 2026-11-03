package app_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestDevSeedChannel(t *testing.T) {
	e := newEnv(t)
	org := e.org()
	in := app.DevChannelInput{MaxChatID: -7700000001, Title: "Демо", Link: "https://max.ru/demo", Subscribers: 1200}

	c, created, err := e.svc.DevSeedChannel(e.ctx(), org, in)
	if err != nil || !created {
		t.Fatalf("seed: created=%v err=%v", created, err)
	}
	if c.Status != domain.StatusModeration || !c.IsPublic || len(c.MissingPermissions()) != 0 ||
		c.SubscribersCount != 1200 || c.Title != "Демо" {
		t.Fatalf("seeded channel: %+v", c)
	}
	again, created, err := e.svc.DevSeedChannel(e.ctx(), org, in)
	if err != nil || created || again.ID != c.ID {
		t.Fatalf("repeat: id=%v created=%v err=%v", again.ID, created, err)
	}
	if n := e.count(`SELECT count(*) FROM channel WHERE max_chat_id = $1`, in.MaxChatID); n != 1 {
		t.Fatalf("channels = %d", n)
	}
	if _, _, err := e.svc.DevSeedChannel(e.ctx(), e.org(), in); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("other organization: %v", err)
	}

	// Дальше — обычный путь кабинета и модерации.
	cat := channeltest.CategoryFood
	if _, err := e.svc.Update(e.ctx(), org, c.ID, domain.ProfileUpdate{CategoryID: &cat}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetPrices(e.ctx(), org, c.ID, f124); err != nil {
		t.Fatal(err)
	}
	adminID, ctx := e.admin()
	view, err := e.svc.Approve(ctx, adminID, c.ID)
	if err != nil || view.Channel.Status != domain.StatusActive {
		t.Fatalf("approve: %+v %v", view, err)
	}
}
