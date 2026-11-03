package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/channeltest"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC).Truncate(time.Microsecond)

func newStore(t *testing.T) (*postgres.Store, *dbtest.DB) {
	d := dbtest.New(t)
	return postgres.New(d.Pool, logx.Discard()), d
}

func TestChannelRoundTripAndLockVersion(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org := channeltest.Organization(t, d.Pool)

	c := domain.NewChannel(ids.New(), -70000000001, 500100, now)
	if err := st.Channels().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := st.Channels().GetByMaxChatIDForUpdate(ctx, -70000000001)
	if err != nil || got.ID != c.ID || got.Status != domain.StatusUnclaimed || got.UnclaimedSince == nil ||
		got.Rating != 100 || got.RknStatus != domain.RknNotRequired || len(got.BotPermissions) != 0 {
		t.Fatalf("read back: %+v %v", got, err)
	}

	got.RightsRefreshed(domain.ChatInfo{Title: "Канал", Link: "https://max.ru/c", IsPublic: true, SubscribersCount: 10},
		domain.Membership{IsAdmin: true, Permissions: []string{"write", "delete"}}, 10000, now)
	if _, err := got.LinkTo(org, 10000, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Channels().Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	stale := *c // прочитан до сохранения: lock_version устарела
	if err := st.Channels().Save(ctx, &stale); !errors.Is(err, domain.ErrStaleVersion) {
		t.Fatalf("stale save: %v", err)
	}

	mine, err := st.Channels().GetForOrganization(ctx, org, c.ID)
	if err != nil || mine.Status != domain.StatusModeration || mine.Title != "Канал" || mine.OrganizationID == nil ||
		mine.UnclaimedSince != nil || len(mine.BotPermissions) != 2 || mine.LockVersion != got.LockVersion {
		t.Fatalf("for organization: %+v %v", mine, err)
	}
	other := channeltest.Organization(t, d.Pool)
	if _, err := st.Channels().GetForOrganization(ctx, other, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign channel: %v", err)
	}
	if list, err := st.Channels().ListByOrganization(ctx, other); err != nil || len(list) != 0 {
		t.Fatalf("foreign list: %d %v", len(list), err)
	}
	if list, err := st.Channels().ListByOrganization(ctx, org); err != nil || len(list) != 1 {
		t.Fatalf("own list: %d %v", len(list), err)
	}

	mine.BotRemoved(now)
	if err := st.Channels().Save(ctx, mine); err != nil {
		t.Fatal(err)
	}
	again, _ := st.Channels().GetForUpdate(ctx, c.ID)
	if again.Status != domain.StatusDisconnected || again.StatusBeforeDisconnect == nil || *again.StatusBeforeDisconnect != domain.StatusModeration {
		t.Fatalf("status_before_disconnect: %+v", again)
	}
}

func TestListUnclaimedBefore(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	old := domain.NewChannel(ids.New(), -1, 1, now.Add(-8*24*time.Hour))
	fresh := domain.NewChannel(ids.New(), -2, 1, now.Add(-time.Hour))
	for _, c := range []*domain.Channel{old, fresh} {
		if err := st.Channels().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Channels().ListUnclaimedBefore(ctx, now.Add(-7*24*time.Hour), 100)
	if err != nil || len(got) != 1 || got[0] != old.ID {
		t.Fatalf("unclaimed: %v %v", got, err)
	}
}

func TestLinkCodes(t *testing.T) {
	st, d := newStore(t)
	ctx := context.Background()
	org := channeltest.Organization(t, d.Pool)
	pepper := []byte("pepper")
	codes := st.LinkCodes()

	first := domain.NewLinkCode(ids.New(), org, "K7M2Q9XA", pepper, time.Hour, now)
	if err := codes.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := codes.ConsumeLive(ctx, org, now); err != nil {
		t.Fatal(err)
	}
	second := domain.NewLinkCode(ids.New(), org, "K7M2Q9XA", pepper, time.Hour, now.Add(time.Second))
	if err := codes.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	// Одинаковый хеш у погашенного и живого кода: находится живой.
	got, err := codes.FindLiveForUpdate(ctx, domain.HashLinkCode(pepper, "K7M2Q9XA"), now.Add(time.Minute))
	if err != nil || got.ID != second.ID || got.OrganizationID != org {
		t.Fatalf("find: %+v %v", got, err)
	}
	if _, err := codes.FindLiveForUpdate(ctx, domain.HashLinkCode(pepper, "K7M2Q9XA"), now.Add(2*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired code found: %v", err)
	}
	got.Consume(now.Add(time.Minute))
	if err := codes.Consume(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := codes.FindLiveForUpdate(ctx, domain.HashLinkCode(pepper, "K7M2Q9XA"), now.Add(time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("consumed code found: %v", err)
	}
	if n, err := codes.CountSince(ctx, org, now.Add(-time.Hour)); err != nil || n != 2 {
		t.Fatalf("count: %d %v", n, err)
	}
}

func TestCategories(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	list, err := st.Categories().ListActive(ctx)
	if err != nil || len(list) != 8 || list[0].Slug != "food" {
		t.Fatalf("categories: %+v %v", list, err)
	}
	if ok, err := st.Categories().ExistsActive(ctx, channeltest.CategoryFood); err != nil || !ok {
		t.Fatalf("food: %v %v", ok, err)
	}
	if ok, err := st.Categories().ExistsActive(ctx, ids.New()); err != nil || ok {
		t.Fatalf("unknown: %v %v", ok, err)
	}
}
