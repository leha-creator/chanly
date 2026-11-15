package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

func TestInitialMetricsOnLink(t *testing.T) {
	e := newEnv(t)
	const chatID = -80000000901
	views := 900
	e.max.messages[chatID] = []domain.MessageStat{{ID: "historical", At: start.Add(-48 * time.Hour), Views: &views}}
	c, _ := e.linkedChannel(chatID, allRights)
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1 AND avg_views=900 AND engagement_bp=2142 AND posts_per_week=0.25`, c.ID); n != 1 {
		t.Fatalf("initial snapshot from historical post: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1 AND status='succeeded' AND dedupe_key LIKE 'initial_channel_metrics:%'`, app.KindRefreshChannelMetrics); n != 1 {
		t.Fatalf("completed initial refreshes: %d", n)
	}
	if !strings.Contains(e.logs.String(), "[FIX] initial channel metrics queued") {
		t.Fatal("missing initial metrics log")
	}
}

func TestInitialMetricsAfterRightsCoexistsWithDailyRefresh(t *testing.T) {
	e := newEnv(t)
	const chatID = -80000000902
	c, _ := e.linkedChannel(chatID, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1`, c.ID); n != 0 {
		t.Fatalf("metrics collected before rights were ready: %d", n)
	}
	// A pending daily refresh must not suppress the immediate first snapshot.
	p := app.MetricsPayload{ChannelID: c.ID, Date: domain.MetricsDate(start).Format(time.DateOnly)}
	if err := e.svc.Jobs.Enqueue(t.Context(), job.Job{
		Kind: app.KindRefreshChannelMetrics, DedupeKey: "channel_metrics:" + c.ID.String() + ":" + p.Date,
		Payload: p, RunAt: start.Add(6 * time.Hour), MaxAttempts: app.RefreshMetricsMaxAttempts,
	}); err != nil {
		t.Fatal(err)
	}
	views := 600
	e.max.messages[chatID] = []domain.MessageStat{{ID: "historical", At: start.Add(-48 * time.Hour), Views: &views}}
	e.max.set(chatID, publicCh, allRights)
	for range 2 {
		if err := e.svc.RefreshChannelInfo(t.Context(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1 AND avg_views=600`, c.ID); n != 1 {
		t.Fatalf("snapshot while daily refresh is delayed: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1 AND status='pending' AND dedupe_key LIKE 'channel_metrics:%'`, app.KindRefreshChannelMetrics); n != 1 {
		t.Fatalf("pending daily refresh: %d", n)
	}
	// Moderation -> active and ordinary profile/rights saves must not repeatedly
	// request the initial snapshot after the first job has already completed.
	cat := channeltest.CategoryFood
	if _, err := e.svc.Update(e.ctx(), *c.OrganizationID, c.ID, domain.ProfileUpdate{CategoryID: &cat}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetPrices(e.ctx(), *c.OrganizationID, c.ID, f124); err != nil {
		t.Fatal(err)
	}
	adminID, adminCtx := e.admin()
	if _, err := e.svc.Approve(adminCtx, adminID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RefreshChannelInfo(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ScheduleMetrics(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1`, app.KindRefreshChannelMetrics); n != 2 {
		t.Fatalf("duplicate initial or daily refresh: %d", n)
	}
	// The regular job and a repeated delivery overwrite the same daily snapshot.
	e.clk.Advance(6 * time.Hour)
	e.drain()
	if err := e.svc.RefreshMetrics(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	card, err := e.svc.CatalogChannel(t.Context(), c.ID)
	if err != nil || len(card.History) != 1 || card.Metrics == nil || card.Metrics.AvgViews == nil || *card.Metrics.AvgViews != 600 {
		t.Fatalf("catalog snapshot after repeated refresh: %+v %v", card, err)
	}
}

func TestInitialMetricsEnqueueFailureRollsBackEligibility(t *testing.T) {
	e := newEnv(t)
	c, _ := e.linkedChannel(-80000000903, domain.Membership{IsAdmin: true, Permissions: []string{"write"}})
	e.max.set(c.MaxChatID, publicCh, allRights)
	queue := e.svc.Jobs
	e.svc.Jobs = saveFailJobs{}
	if err := e.svc.RefreshChannelInfo(t.Context(), c.ID); !errors.Is(err, errSaveInjected) {
		t.Fatalf("enqueue failure: %v", err)
	}
	if got := e.channel(c.MaxChatID); got.Status != c.Status || got.LockVersion != c.LockVersion {
		t.Fatalf("failed enqueue committed eligibility: %+v", got)
	}
	e.svc.Jobs = queue
	if err := e.svc.RefreshChannelInfo(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1`, c.ID); n != 1 {
		t.Fatalf("snapshot after successful retry: %d", n)
	}
}

func TestInitialMetricsRetriesProviderFailure(t *testing.T) {
	e := newEnv(t)
	e.max.failMessages = errors.New("temporary MAX failure")
	c, _ := e.linkedChannel(-80000000904, allRights)
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1 AND status='pending' AND attempts=1`, app.KindRefreshChannelMetrics); n != 1 {
		t.Fatalf("initial refresh not retried: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1`, c.ID); n != 0 {
		t.Fatalf("failed provider call wrote snapshot: %d", n)
	}
	e.max.failMessages = nil
	e.clk.Advance(time.Minute)
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1`, c.ID); n != 1 {
		t.Fatalf("retry did not produce snapshot: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1`, app.KindRefreshChannelMetrics); n != 1 {
		t.Fatalf("retry created extra jobs: %d", n)
	}
}

func TestInitialMetricsReconnectWaitsForRights(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000905)
	e.clk.Advance(time.Minute)
	if err := e.svc.BotRemoved(t.Context(), c.MaxChatID); err != nil {
		t.Fatal(err)
	}
	views := 300
	e.max.messages[c.MaxChatID] = []domain.MessageStat{{ID: "historical", At: start.Add(-48 * time.Hour), Views: &views}}
	if err := e.svc.BotAdded(t.Context(), app.EventRef{ID: ids.New(), ReceivedAt: start}, c.MaxChatID, 500100, true); err != nil {
		t.Fatal(err)
	}
	if got := e.channel(c.MaxChatID); got.Status != domain.StatusSuspended || got.BotIsAdmin {
		t.Fatalf("reconnected channel must await authoritative rights: %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1 AND status='pending'`, app.KindRefreshChannelMetrics); n != 0 {
		t.Fatalf("metrics queued before bot rights were read: %d", n)
	}
	e.drain()
	if got := e.channel(c.MaxChatID); got.Status != domain.StatusActive {
		t.Fatalf("channel not restored: %s", got.Status)
	}
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1 AND avg_views=300`, c.ID); n != 1 {
		t.Fatalf("missing refreshed snapshot on reconnect: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM job WHERE kind=$1 AND status='succeeded'`, app.KindRefreshChannelMetrics); n != 2 {
		t.Fatalf("one initial refresh per authoritative connection expected: %d", n)
	}
}

func TestInitialMetricsOnAdminUnblock(t *testing.T) {
	e := newEnv(t)
	c := e.active(-80000000906)
	e.clk.Advance(time.Minute)
	adminID, adminCtx := e.admin()
	if _, err := e.svc.Block(adminCtx, adminID, c.ID, "review"); err != nil {
		t.Fatal(err)
	}
	views := 1200
	e.max.messages[c.MaxChatID] = []domain.MessageStat{{ID: "historical", At: start.Add(-48 * time.Hour), Views: &views}}
	if _, err := e.svc.Unblock(adminCtx, adminID, c.ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT count(*) FROM channel_metrics WHERE channel_id=$1 AND avg_views=1200`, c.ID); n != 1 {
		t.Fatalf("snapshot not refreshed on unblock: %d", n)
	}
	if len(e.avail.lost) != 0 || len(e.avail.restored) != 0 {
		t.Fatalf("block/unblock changed placement availability: lost=%d restored=%d", len(e.avail.lost), len(e.avail.restored))
	}
}
