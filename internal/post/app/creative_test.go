package app_test

import (
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/post/domain"
)

func TestCreativeContent(t *testing.T) {
	e := newEnv(t)
	c := e.readyDraft(domain.CarrierText)
	v, err := e.svc.CreateVersion(e.ctx(), e.org, c.ID, e.user, 42)
	if err != nil {
		t.Fatal(err)
	}
	// R-9: до публикации производной публичного адреса нет.
	cc, err := e.svc.CreativeContent(e.ctx(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cc.VersionID != v.ID || cc.OrganizationID != e.org || cc.Status != domain.VersionPendingModeration ||
		len(cc.ImageURLs) != 1 || cc.ImageURLs[0] != "" {
		t.Fatalf("before publish: %+v", cc)
	}
	if cc.Text == "" || strings.ContainsAny(cc.Text, "<>") {
		t.Fatalf("plain text %q", cc.Text)
	}
	e.drain()
	cc, err = e.svc.CreativeContent(e.ctx(), v.ID)
	if err != nil || cc.ImageURLs[0] == "" {
		t.Fatalf("after publish: %+v %v", cc, err)
	}
	for _, u := range cc.URLs {
		if !strings.Contains(u, "utm_campaign=order_42") {
			t.Fatalf("link without UTM: %s", u)
		}
	}
}
