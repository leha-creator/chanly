package domain

import (
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	t0    = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	orgA  = uuid.MustParse("0190a000-0000-7000-8000-00000000000a")
	orgB  = uuid.MustParse("0190a000-0000-7000-8000-00000000000b")
	allOK = Membership{IsAdmin: true, Permissions: []string{"write", "delete", "edit"}}
	pub   = ChatInfo{Title: "Канал", IsPublic: true, IsChannel: true, SubscribersCount: 1200}
)

// channelIn — связанный канал в заданном состоянии с правами в порядке.
func channelIn(s Status) *Channel {
	c := NewChannel(uuid.New(), -100, 7, t0)
	if s != StatusUnclaimed {
		org := orgA
		c.OrganizationID = &org
		c.UnclaimedSince = nil
	}
	c.Status = s
	c.IsPublic = true
	c.BotIsAdmin = true
	c.BotPermissions = []string{"write", "delete"}
	if s == StatusActive || s == StatusSuspended {
		at := t0.Add(-time.Hour)
		c.ModeratedAt = &at
		cat := uuid.New()
		c.CategoryID = &cat
	}
	if s == StatusSuspended {
		c.StatusReason = ReasonMissingRights
	}
	return c
}

func TestRightsRefreshedResolvesStatus(t *testing.T) {
	noDelete := Membership{IsAdmin: true, Permissions: []string{"write"}}
	closed := pub
	closed.IsPublic = false
	cases := []struct {
		from       Status
		info       ChatInfo
		m          Membership
		want       Status
		wantReason string
	}{
		{StatusUnclaimed, pub, allOK, StatusUnclaimed, ""},
		{StatusUnclaimed, pub, noDelete, StatusUnclaimed, ""},
		{StatusPendingRights, pub, allOK, StatusModeration, ""},
		{StatusPendingRights, pub, noDelete, StatusPendingRights, ReasonMissingRights},
		{StatusPendingRights, closed, allOK, StatusModeration, ""},
		{StatusModeration, pub, allOK, StatusModeration, ""},
		{StatusModeration, pub, noDelete, StatusPendingRights, ReasonMissingRights},
		{StatusActive, pub, allOK, StatusActive, ""},
		{StatusActive, pub, noDelete, StatusSuspended, ReasonMissingRights},
		{StatusActive, closed, allOK, StatusActive, ""},
		{StatusActive, pub, Membership{}, StatusSuspended, ReasonMissingRights},
		{StatusSuspended, pub, allOK, StatusActive, ""},
		{StatusSuspended, pub, noDelete, StatusSuspended, ReasonMissingRights},
		{StatusRejected, pub, allOK, StatusRejected, ""},
		{StatusRejected, pub, noDelete, StatusRejected, ""},
		{StatusBlocked, pub, allOK, StatusBlocked, ""},
		{StatusBlocked, pub, noDelete, StatusBlocked, ""},
		{StatusDisconnected, pub, allOK, StatusDisconnected, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"/"+tc.wantReason+"/"+string(tc.want), func(t *testing.T) {
			c := channelIn(tc.from)
			change := c.RightsRefreshed(tc.info, tc.m, 10000, t0)
			if c.Status != tc.want || c.StatusReason != tc.wantReason {
				t.Fatalf("status = %s/%q, want %s/%q", c.Status, c.StatusReason, tc.want, tc.wantReason)
			}
			if change.From != tc.from || change.To != tc.want {
				t.Fatalf("change = %+v", change)
			}
			if c.BotPermissionsCheckedAt == nil || !c.UpdatedAt.Equal(t0) {
				t.Fatalf("checked_at %v, updated_at %v", c.BotPermissionsCheckedAt, c.UpdatedAt)
			}
		})
	}
}

func TestSuspendedForOtherReasonStays(t *testing.T) {
	// rkn_required снимается сам, когда номер подтверждён или подписчиков меньше порога
	// (R-16 этапа «Каталог», TestRknRule); причину, которую ставит администратор, — нет.
	c := channelIn(StatusSuspended)
	c.StatusReason = "admin_hold"
	c.RightsRefreshed(pub, allOK, 10000, t0)
	if c.Status != StatusSuspended || c.StatusReason != "admin_hold" {
		t.Fatalf("status = %s/%s", c.Status, c.StatusReason)
	}
}

func TestAvailabilityFlags(t *testing.T) {
	c := channelIn(StatusActive)
	lost := c.RightsRefreshed(pub, Membership{IsAdmin: true, Permissions: []string{"write"}}, 10000, t0)
	if !lost.AvailabilityLost() || lost.AvailabilityRestored() {
		t.Fatalf("lost = %+v", lost)
	}
	back := c.RightsRefreshed(pub, allOK, 10000, t0)
	if !back.AvailabilityRestored() || back.AvailabilityLost() || c.Status != StatusActive {
		t.Fatalf("restored = %+v", back)
	}
	same := c.RightsRefreshed(pub, allOK, 10000, t0)
	if same.Changed() || same.AvailabilityRestored() {
		t.Fatalf("no-op change = %+v", same)
	}
}

func TestMissingPermissions(t *testing.T) {
	c := channelIn(StatusModeration)
	c.BotPermissions = []string{"write", "edit"}
	if got := c.MissingPermissions(); !slices.Equal(got, []string{"delete"}) {
		t.Fatalf("missing = %v", got)
	}
	c.BotIsAdmin = false
	if got := c.MissingPermissions(); !slices.Equal(got, []string{"write", "delete"}) {
		t.Fatalf("missing when not admin = %v", got)
	}
}

func TestBotRemovedAndReAdded(t *testing.T) {
	cases := []struct {
		before    Status
		afterAdd  Status
		afterOK   Status
		withOwner bool
	}{
		{StatusActive, StatusSuspended, StatusActive, true},
		{StatusSuspended, StatusSuspended, StatusActive, true},
		{StatusModeration, StatusPendingRights, StatusModeration, true},
		{StatusPendingRights, StatusPendingRights, StatusModeration, true},
		{StatusRejected, StatusPendingRights, StatusModeration, true},
		{StatusUnclaimed, StatusUnclaimed, StatusUnclaimed, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.before), func(t *testing.T) {
			c := channelIn(tc.before)
			removed := c.BotRemoved(t0)
			if c.Status != StatusDisconnected || removed.From != tc.before || c.BotIsAdmin || len(c.BotPermissions) != 0 {
				t.Fatalf("after remove: %+v, %+v", c, removed)
			}
			if c.StatusBeforeDisconnect == nil || *c.StatusBeforeDisconnect != tc.before {
				t.Fatalf("status_before_disconnect = %v", c.StatusBeforeDisconnect)
			}
			if again := c.BotRemoved(t0); again.Changed() {
				t.Fatal("second bot_removed changed the channel")
			}
			added := c.BotAdded(99, t0.Add(time.Hour))
			if c.Status != tc.afterAdd || added.From != StatusDisconnected || c.AddedByMaxUserID != 99 || c.StatusBeforeDisconnect != nil {
				t.Fatalf("after add: status %s, change %+v", c.Status, added)
			}
			if (c.OrganizationID != nil) != tc.withOwner {
				t.Fatalf("owner kept = %v, want %v", c.OrganizationID != nil, tc.withOwner)
			}
			c.RightsRefreshed(pub, allOK, 10000, t0.Add(2*time.Hour))
			if c.Status != tc.afterOK {
				t.Fatalf("after refresh: %s, want %s", c.Status, tc.afterOK)
			}
		})
	}
}

func TestBotRemovedFromBlockedKeepsBlocked(t *testing.T) {
	c := channelIn(StatusBlocked)
	c.BotRemoved(t0)
	if c.Status != StatusBlocked || c.BotIsAdmin {
		t.Fatalf("status = %s, admin = %v", c.Status, c.BotIsAdmin)
	}
}

func TestBotAddedToLinkedChannelKeepsAddedBy(t *testing.T) {
	c := channelIn(StatusActive)
	if ch := c.BotAdded(42, t0); ch.Changed() || c.AddedByMaxUserID != 7 {
		t.Fatalf("change %+v, added_by %d", ch, c.AddedByMaxUserID)
	}
	u := channelIn(StatusUnclaimed)
	u.BotAdded(42, t0)
	if u.AddedByMaxUserID != 42 || u.Status != StatusUnclaimed {
		t.Fatalf("unclaimed: added_by %d, status %s", u.AddedByMaxUserID, u.Status)
	}
}

func TestLinkTo(t *testing.T) {
	c := NewChannel(uuid.New(), -100, 7, t0)
	c.RightsRefreshed(pub, allOK, 10000, t0)
	change, err := c.LinkTo(orgA, 10000, t0)
	if err != nil || c.Status != StatusModeration || change.From != StatusUnclaimed || change.To != StatusModeration {
		t.Fatalf("link: err %v, status %s, change %+v", err, c.Status, change)
	}
	if c.UnclaimedSince != nil || *c.OrganizationID != orgA {
		t.Fatal("unclaimed_since or organization not updated")
	}
	if _, err := c.LinkTo(orgA, 10000, t0); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("same org: %v", err)
	}
	if _, err := c.LinkTo(orgB, 10000, t0); !errors.Is(err, ErrLinkedToOther) || *c.OrganizationID != orgA {
		t.Fatalf("other org: %v", err)
	}

	noRights := NewChannel(uuid.New(), -101, 7, t0)
	noRights.RightsRefreshed(pub, Membership{IsAdmin: true, Permissions: []string{"write"}}, 10000, t0)
	if _, err := noRights.LinkTo(orgA, 10000, t0); err != nil || noRights.Status != StatusPendingRights {
		t.Fatalf("without delete: %v, %s", err, noRights.Status)
	}

	released := NewChannel(uuid.New(), -102, 7, t0)
	released.ReleaseUnclaimed(t0)
	if _, err := released.LinkTo(orgA, 10000, t0); !errors.Is(err, ErrNotLinkable) {
		t.Fatalf("released: %v", err)
	}
}

func TestReleaseUnclaimed(t *testing.T) {
	c := NewChannel(uuid.New(), -100, 7, t0)
	change, ok := c.ReleaseUnclaimed(t0)
	if !ok || c.Status != StatusDisconnected || c.StatusReason != ReasonUnclaimedExpired || change.From != StatusUnclaimed {
		t.Fatalf("release: %v %+v", ok, c)
	}
	c.BotAdded(8, t0)
	if c.Status != StatusUnclaimed || c.UnclaimedSince == nil {
		t.Fatalf("re-added released channel: %s", c.Status)
	}
	if _, ok := channelIn(StatusModeration).ReleaseUnclaimed(t0); ok {
		t.Fatal("linked channel released")
	}
}

func TestUpdateProfile(t *testing.T) {
	desc, num := "Рецепты", "4930000000"
	cat := uuid.New()
	for _, s := range []Status{StatusUnclaimed, StatusDisconnected, StatusBlocked} {
		if _, err := channelIn(s).UpdateProfile(ProfileUpdate{Description: &desc}, 10000, t0); !errors.Is(err, ErrNotEditable) {
			t.Fatalf("%s: %v", s, err)
		}
	}

	rejected := channelIn(StatusRejected)
	rejected.StatusReason = "плохие посты"
	change, err := rejected.UpdateProfile(ProfileUpdate{Description: &desc, CategoryID: &cat}, 10000, t0)
	if err != nil || rejected.Status != StatusModeration || change.From != StatusRejected || rejected.StatusReason != "" {
		t.Fatalf("rejected → %s (%v), %+v", rejected.Status, err, change)
	}
	if rejected.Description != desc || *rejected.CategoryID != cat {
		t.Fatal("fields not applied")
	}

	pending := channelIn(StatusPendingRights)
	pending.BotPermissions = []string{"write"}
	pending.StatusReason = ReasonMissingRights
	if _, err := pending.UpdateProfile(ProfileUpdate{Description: &desc}, 10000, t0); err != nil || pending.Status != StatusPendingRights {
		t.Fatalf("pending without rights → %s (%v)", pending.Status, err)
	}

	for _, tc := range []struct {
		subs   int
		number *string
		cur    RknStatus
		want   RknStatus
	}{
		{9999, &num, RknNotRequired, RknNotRequired},
		{10000, &num, RknNotRequired, RknPending},
		{10000, nil, RknNotRequired, RknPending},
		{12000, nil, RknConfirmed, RknConfirmed},
		{12000, &num, RknConfirmed, RknPending},
		{12000, nil, RknRejected, RknRejected},
	} {
		c := channelIn(StatusModeration)
		c.SubscribersCount = tc.subs
		c.RknStatus = tc.cur
		if tc.cur == RknConfirmed {
			c.RknNumber = "old"
		}
		if _, err := c.UpdateProfile(ProfileUpdate{RknNumber: tc.number}, 10000, t0); err != nil {
			t.Fatal(err)
		}
		if c.RknStatus != tc.want {
			t.Errorf("subs %d, number %v, %s → %s, want %s", tc.subs, tc.number != nil, tc.cur, c.RknStatus, tc.want)
		}
	}
}

// TestStatusesMatchDomainModel сверяет перечисление с 04 §9 (ARCHITECTURE.md, «Тестирование»).
func TestStatusesMatchDomainModel(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/04-domain-model.md")
	if err != nil {
		t.Skipf("docs not available: %v", err)
	}
	line := regexp.MustCompile("(?m)^\\| `channel.status` \\|([^\r\n]*)").FindSubmatch(doc)
	if line == nil {
		t.Fatal("channel.status row not found in 04 §9")
	}
	var want []Status
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllSubmatch(line[1], -1) {
		want = append(want, Status(m[1]))
	}
	if !slices.Equal(want, Statuses) {
		t.Fatalf("04 §9: %v, code: %v", want, Statuses)
	}
}

func TestLinkCode(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := GenerateLinkCode()
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := NormalizeLinkCode(code); !ok || n != code {
			t.Fatalf("generated code %q does not normalize", code)
		}
		seen[code] = true
	}
	if len(seen) < 199 {
		t.Fatalf("only %d distinct codes of 200", len(seen))
	}
	for text, want := range map[string]string{
		"k7m2q9xa":             "K7M2Q9XA",
		"  K7M2Q9XA \n":        "K7M2Q9XA",
		"K7M2Q9X":              "",
		"K7M2Q9XAB":            "",
		"K7M2Q9X0":             "",
		"K7M2Q9XO":             "",
		"K7M2 9XA":             "",
		"привет, канал!":       "",
		strings.Repeat("A", 8): "AAAAAAAA",
	} {
		got, ok := NormalizeLinkCode(text)
		if ok != (want != "") || got != want {
			t.Errorf("NormalizeLinkCode(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	pepper := []byte("pepper")
	if HashLinkCode(pepper, "K7M2Q9XA") != HashLinkCode(pepper, "K7M2Q9XA") {
		t.Fatal("hash is not stable")
	}
	if HashLinkCode(pepper, "K7M2Q9XA") == HashLinkCode(pepper, "K7M2Q9XB") || HashLinkCode(pepper, "K7M2Q9XA") == HashLinkCode([]byte("x"), "K7M2Q9XA") {
		t.Fatal("hash collision")
	}
	c := NewLinkCode(uuid.New(), orgA, "K7M2Q9XA", pepper, time.Hour, t0)
	if !c.Live(t0.Add(59*time.Minute)) || c.Live(t0.Add(time.Hour)) {
		t.Fatal("ttl boundary")
	}
	c.Consume(t0)
	if c.Live(t0) {
		t.Fatal("consumed code is live")
	}
}

func TestStatusChangeNoticeTemplate(t *testing.T) {
	for _, tc := range []struct {
		from, to Status
		reason   string
		want     string
	}{
		{StatusModeration, StatusActive, "", NoticeApproved},
		{StatusSuspended, StatusActive, "", NoticeRestored},
		{StatusModeration, StatusRejected, "Нет описания", NoticeRejected},
		{StatusActive, StatusSuspended, ReasonMissingRights, NoticeSuspended},
		{StatusActive, StatusDisconnected, "", NoticeDisconnected},
		{StatusUnclaimed, StatusDisconnected, ReasonUnclaimedExpired, ""},
		{StatusPendingRights, StatusModeration, "", ""},
		{StatusActive, StatusActive, "", ""},
		{StatusActive, StatusBlocked, ReasonBlockedByAdmin, NoticeBlocked},
	} {
		got, ok := StatusChange{From: tc.from, To: tc.to, Reason: tc.reason}.NoticeTemplate()
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s → %s (%s): %q %v, want %q", tc.from, tc.to, tc.reason, got, ok, tc.want)
		}
	}
}
