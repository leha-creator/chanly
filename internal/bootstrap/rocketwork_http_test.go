package bootstrap

import (
	"context"
	"net/http"
	"testing"

	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
)

func TestRocketWorkExplicitActionOwnershipAndReplay(t *testing.T) {
	a := newTestApp(t)
	owner, org := a.newBlogger("rw-action-owner@example.test")
	_, other := a.newBlogger("rw-action-other@example.test")
	a.mods.payout.Config.ProviderScope = &payoutdomain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	path := "/blogger/rocketwork/actions"
	if r := a.client().do(http.MethodPost, path, map[string]any{"action": "invite"}); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.status)
	}
	advertiser := a.advertiserClient("rw-action-advertiser@example.test")
	if r := advertiser.do(http.MethodPost, path, map[string]any{"action": "invite"}); r.status != http.StatusForbidden {
		t.Fatalf("wrong cabinet: %d", r.status)
	}
	for i := 0; i < 2; i++ {
		r := owner.do(http.MethodPost, path, map[string]any{"action": "invite", "organization_id": other.String()})
		if r.status != http.StatusAccepted {
			t.Fatalf("invite: %d %s", r.status, r.body)
		}
		var state struct {
			NextAction string `json:"next_action"`
			URL        string `json:"onboarding_url"`
		}
		r.json(t, &state)
		if state.NextAction != "complete_registration" || state.URL != "" {
			t.Fatalf("wrong next action: %+v", state)
		}
	}
	var count int
	if err := a.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM rocketwork_onboarding WHERE organization_id=$1", org).Scan(&count); err != nil || count != 1 {
		t.Fatalf("own checkpoint: %d %v", count, err)
	}
	if err := a.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM rocketwork_onboarding WHERE organization_id=$1", other).Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign organization changed")
	}
	if r := owner.do(http.MethodPost, path, map[string]any{"action": "register", "first_name": "Synthetic", "last_name": "Person"}); r.status != http.StatusConflict {
		t.Fatalf("self-employed API registration: %d", r.status)
	}
}
