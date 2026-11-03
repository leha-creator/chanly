package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
)

func TestPlacementConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Placement
	if p.AcceptTimeout != 24*time.Hour || p.ScheduleTimeout != 24*time.Hour || p.WindowGuard != time.Hour ||
		p.MinLead != time.Hour || p.SlotStep != 5*time.Minute || p.ReeditGuard != 24*time.Hour || p.CartMaxItems != 50 ||
		p.StuckScheduledAge != 35*time.Minute {
		t.Fatalf("placement defaults: %+v", p)
	}
	for _, tc := range []struct {
		key, value, field string
		ok                bool
	}{
		{"PLACEMENT_SLOT_STEP", "7m", "PLACEMENT_SLOT_STEP", false},
		{"PLACEMENT_SLOT_STEP", "90s", "PLACEMENT_SLOT_STEP", false},
		{"PLACEMENT_SLOT_STEP", "1m", "", true},
		{"PLACEMENT_SLOT_STEP", "60m", "", true},
		{"PLACEMENT_SLOT_STEP", "0s", "PLACEMENT_SLOT_STEP", false},
		{"PLACEMENT_WINDOW_GUARD", "24h", "", true},
		{"PLACEMENT_WINDOW_GUARD", "25h", "PLACEMENT_WINDOW_GUARD", false},
		{"PLACEMENT_ACCEPT_TIMEOUT", "0s", "PLACEMENT_ACCEPT_TIMEOUT", false},
		{"PLACEMENT_REEDIT_GUARD", "-1h", "PLACEMENT_REEDIT_GUARD", false},
		{"CART_MAX_ITEMS", "0", "CART_MAX_ITEMS", false},
		{"CART_MAX_ITEMS", "200", "", true},
		{"CART_MAX_ITEMS", "201", "CART_MAX_ITEMS", false},
	} {
		env := validAPIEnv()
		env[tc.key] = tc.value
		_, err := LoadConfig(env, ModeAPI)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		case !tc.ok && (err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, tc.field) })):
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
	}
	if _, err := LoadConfig(map[string]string{"DATABASE_URL": "postgres://x"}, ModeDev); err != nil {
		t.Errorf("dev mode needs only the database: %v", err)
	}
	if !strings.Contains(cfg.LogValue().String(), "placement_slot_step") {
		t.Error("placement settings are not logged")
	}
}

func TestPlacementWiring(t *testing.T) {
	a := newTestApp(t)
	if !slices.Contains(a.worker.Kinds(), "placement_timer") {
		t.Fatalf("worker kinds %v have no placement_timer", a.worker.Kinds())
	}
	// Этап «Маркировка», R-11: сначала модуль размещений, затем маркировка.
	ls, ok := a.mods.post.Listener.(bridge.VersionListeners)
	if !ok || len(ls) != 2 {
		t.Fatalf("post version listener %T", a.mods.post.Listener)
	}
	if _, ok := ls[0].(bridge.VersionListener); !ok {
		t.Fatalf("first version listener %T", ls[0])
	}
	if _, ok := ls[1].(bridge.MarkingVersionListener); !ok {
		t.Fatalf("second version listener %T", ls[1])
	}
	if _, ok := a.mods.channel.Availability.(bridge.ChannelPlacements); !ok {
		t.Fatalf("channel availability listener %T", a.mods.channel.Availability)
	}
	if _, ok := a.mods.channel.Placements.(bridge.ChannelPlacements); !ok {
		t.Fatalf("channel placement schedule %T", a.mods.channel.Placements)
	}
	if _, ok := a.mods.channel.Cart.(bridge.ChannelPlacements); !ok {
		t.Fatalf("channel cart lookup %T", a.mods.channel.Cart)
	}
	if _, ok := a.mods.placement.Holds.(bridge.LedgerHolds); !ok {
		t.Fatalf("holds %T", a.mods.placement.Holds)
	}
	if _, ok := a.mods.placement.Ratings.(bridge.PlacementChannelRatings); !ok || a.mods.placement.Claims == nil {
		t.Fatalf("ratings %T claims %T", a.mods.placement.Ratings, a.mods.placement.Claims)
	}

	// Операции этапа отвечают не 501, включая приёмку и претензию (этап «Приёмка, споры и возвраты»).
	c := a.client()
	c.login("anyone-orders@example.ru")
	id := "0190a000-0000-7000-8000-000000000999"
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/cart"}, {http.MethodGet, "/orders"}, {http.MethodGet, "/orders/" + id},
		{http.MethodPost, "/orders/" + id + "/cancel"}, {http.MethodPost, "/orders/" + id + "/post-versions"},
		{http.MethodPost, "/placements/" + id + "/cancel"}, {http.MethodGet, "/blogger/placements"},
		{http.MethodGet, "/blogger/placements/" + id}, {http.MethodPost, "/blogger/placements/" + id + "/accept"},
		{http.MethodPost, "/campaigns/" + id + "/checkout"}, {http.MethodPost, "/placements/" + id + "/accept"},
		{http.MethodPost, "/placements/" + id + "/claims"},
	} {
		if r := c.do(op.method, op.path, nil); r.status == http.StatusNotImplemented {
			t.Errorf("%s %s: 501", op.method, op.path)
		}
	}
}

func TestDevPostVersion(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	prod := testConfig()
	prod.Env = EnvProduction
	if err := runDev(ctx, prod, a.mods.in.log, []string{"post-version", "--order", "1001", "approve"}, &bytes.Buffer{}); !errors.Is(err, errDevForbidden) {
		t.Fatalf("production: %v", err)
	}
	for _, args := range [][]string{{}, {"other"}, {"post-version", "approve"}, {"post-version", "--order", "1001", "publish"}} {
		if err := runDev(ctx, testConfig(), a.mods.in.log, args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}

	adv := a.advertiserClient("adv-dev@example.ru")
	blg := a.bloggerClient("blg-dev@example.ru")
	ch := a.activeChannel(blg, "Канал", 1_000_000)
	campaign := a.campaignWithPost(adv, "Кампания")
	adv.do(http.MethodPost, "/cart/items", map[string]any{"channel_id": ch, "format_id": "f_1_24"})
	var order orderView
	adv.do(http.MethodPost, "/campaigns/"+campaign+"/checkout", nil).json(t, &order)

	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	if err := runDev(ctx, cfg, a.mods.in.log, []string{"post-version", "--order", "1001", "approve"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "order 1001 status marking") {
		t.Fatalf("output %q", out.String())
	}
	if !strings.Contains(a.logs.String(), `"msg":"dev command used"`) {
		t.Fatal("dev command not logged")
	}
}

func TestPlacementControlConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	k := cfg.Placement.Control
	if k.PublishRetryWindow != 30*time.Minute || k.PublishRetryInterval != 30*time.Second || k.PublishFindLookback != 40*time.Minute ||
		k.PublishMaxImages != 10 || k.RemoveRetryWindow != 24*time.Hour || k.ClaimWindow != 24*time.Hour ||
		k.DisputeDeadline != 72*time.Hour || k.TopViolationShare != 5000 || k.ReconcileInterval != 5*time.Minute ||
		k.ScheduledGrace != 5*time.Minute || k.ViewsRefresh != time.Hour || k.StuckRemovalOverdueAge != time.Hour {
		t.Fatalf("control defaults: %+v", k)
	}
	if r := controlRules(k); r.MaxImages != 10 || r.TopViolationShare != 5000 || r.PublishRetryWindow != 30*time.Minute ||
		r.RatingPenaltyRefund != 10 || r.RatingPenaltyProportional != 5 {
		t.Fatalf("rules %+v", r)
	}
	for _, tc := range []struct {
		key, value, field string
		ok                bool
	}{
		{"PUBLISH_RETRY_INTERVAL", "30m", "PUBLISH_RETRY_INTERVAL", false},
		{"PUBLISH_RETRY_INTERVAL", "29m", "", true},
		{"PUBLISH_FIND_LOOKBACK", "30m", "PUBLISH_FIND_LOOKBACK", false},
		{"PUBLISH_MAX_IMAGES", "0", "PUBLISH_MAX_IMAGES", false},
		{"PUBLISH_MAX_IMAGES", "11", "PUBLISH_MAX_IMAGES", false},
		{"DISPUTE_TOP_VIOLATION_SHARE", "10000", "", true},
		{"DISPUTE_TOP_VIOLATION_SHARE", "10001", "DISPUTE_TOP_VIOLATION_SHARE", false},
		{"DISPUTE_RATING_PENALTY_REFUND", "0", "", true},
		{"DISPUTE_RATING_PENALTY_REFUND", "101", "DISPUTE_RATING_PENALTY_REFUND", false},
		{"DISPUTE_RATING_PENALTY_PROPORTIONAL", "100", "", true},
		{"DISPUTE_RATING_PENALTY_PROPORTIONAL", "-1", "DISPUTE_RATING_PENALTY_PROPORTIONAL", false},
		{"RECONCILE_INTERVAL", "0s", "RECONCILE_INTERVAL", false},
		{"REMOVE_RETRY_WINDOW", "-1h", "REMOVE_RETRY_WINDOW", false},
		{"MAX_BOT_USER_ID", "123", "", true},
	} {
		env := validAPIEnv()
		env[tc.key] = tc.value
		_, err := LoadConfig(env, ModeAPI)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		case !tc.ok && (err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, tc.field) })):
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
	}
}

func TestAutopostingWiring(t *testing.T) {
	a := newTestApp(t)
	for _, kind := range []string{"publish_placement", "remove_placement", "check_publication", "reconcile_publications"} {
		if !slices.Contains(a.worker.Kinds(), kind) {
			t.Errorf("worker kinds have no %s", kind)
		}
	}
	if _, ok := a.mods.channel.PostListener.(bridge.ChannelPosts); !ok {
		t.Fatalf("channel post listener %T", a.mods.channel.PostListener)
	}
	if _, ok := a.mods.placement.Publisher.(bridge.PlacementPublisher); !ok {
		t.Fatalf("publisher %T", a.mods.placement.Publisher)
	}
	if _, ok := a.mods.placement.Content.(bridge.PlacementContent); !ok {
		t.Fatalf("content %T", a.mods.placement.Content)
	}
}
