package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func seqIDs() func() uuid.UUID {
	return func() uuid.UUID { return uuid.Must(uuid.NewV7()) }
}

func countBy(ns []Notification) map[Channel]int {
	out := map[Channel]int{}
	for _, n := range ns {
		out[n.Channel]++
	}
	return out
}

func TestCatalogCodesUniqueAndHaveRoles(t *testing.T) {
	seen := map[string]bool{}
	for _, tpl := range Catalog() {
		if seen[tpl.Code] {
			t.Fatalf("duplicate code %s", tpl.Code)
		}
		seen[tpl.Code] = true
		if len(tpl.Roles) == 0 {
			t.Fatalf("%s has no roles", tpl.Code)
		}
	}
	if _, err := Lookup("nope"); !errors.Is(err, ErrUnknownTemplate) {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestPlanChannelsByRole(t *testing.T) {
	owner, second := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	maxUser := int64(777)
	org := uuid.Must(uuid.NewV7())
	cases := []struct {
		name     string
		notice   Notice
		audience Audience
		want     map[Channel]int
	}{
		{"blogger with max user", Notice{Template: "placement.request", Role: RoleBlogger, OrganizationID: org, ObjectType: "placement", ObjectID: "p1"},
			Audience{UserIDs: []uuid.UUID{owner}, MaxUserID: &maxUser}, map[Channel]int{ChannelCabinet: 1, ChannelMaxBot: 1, ChannelEmail: 1}},
		{"blogger without max user", Notice{Template: "placement.request", Role: RoleBlogger, OrganizationID: org},
			Audience{UserIDs: []uuid.UUID{owner}}, map[Channel]int{ChannelCabinet: 1, ChannelEmail: 1}},
		{"advertiser two users", Notice{Template: "placement.accepted", Role: RoleAdvertiser, OrganizationID: org},
			Audience{UserIDs: []uuid.UUID{owner, second}, MaxUserID: &maxUser}, map[Channel]int{ChannelCabinet: 2, ChannelEmail: 2}},
		{"admin", Notice{Template: "placement.failed", Role: RoleAdmin},
			Audience{UserIDs: []uuid.UUID{owner, second}}, map[Channel]int{ChannelCabinet: 2}},
		{"nobody", Notice{Template: "placement.failed", Role: RoleAdvertiser, OrganizationID: org},
			Audience{MaxUserID: &maxUser}, map[Channel]int{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns, err := Plan(c.notice, c.audience, seqIDs(), now)
			if err != nil {
				t.Fatal(err)
			}
			got := countBy(ns)
			if len(got) != len(c.want) {
				t.Fatalf("channels %v, want %v", got, c.want)
			}
			for ch, n := range c.want {
				if got[ch] != n {
					t.Fatalf("channels %v, want %v", got, c.want)
				}
			}
			for _, n := range ns {
				switch n.Channel {
				case ChannelCabinet:
					if n.Status != StatusSent || n.SentAt == nil {
						t.Fatalf("cabinet must be sent: %+v", n)
					}
				case ChannelMaxBot:
					if n.Status != StatusPending || n.MaxUserID == nil || *n.MaxUserID != maxUser || n.UserID != owner {
						t.Fatalf("max row: %+v", n)
					}
				default:
					if n.Status != StatusPending {
						t.Fatalf("email must be pending: %+v", n)
					}
				}
			}
		})
	}
}

func TestPlanRejectsWrongRoleAndKeepsKey(t *testing.T) {
	u := uuid.Must(uuid.NewV7())
	if _, err := Plan(Notice{Template: "placement.request", Role: RoleAdvertiser}, Audience{UserIDs: []uuid.UUID{u}}, seqIDs(), now); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("role check: %v", err)
	}
	ns, err := Plan(Notice{Template: "placement.accept_reminder", Role: RoleBlogger, Key: "reminder:p:T-01",
		ObjectType: "placement", ObjectID: "p"}, Audience{UserIDs: []uuid.UUID{u}}, seqIDs(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.DedupeKey == nil || *n.DedupeKey != "reminder:p:T-01" || n.OrganizationID != nil {
			t.Fatalf("key/org: %+v", n)
		}
		if n.Link() != "/blg/placements?placement=p" {
			t.Fatalf("link %q", n.Link())
		}
	}
}

func TestLink(t *testing.T) {
	order := uuid.Must(uuid.NewV7())
	cases := []struct {
		n    Notice
		want string
	}{
		{Notice{Template: "placement.published", Role: RoleAdvertiser, ObjectType: "placement", ObjectID: "p", Data: map[string]any{DataOrderID: order}}, "/adv/orders/" + order.String()},
		{Notice{Template: "placement.published", Role: RoleAdvertiser, ObjectType: "placement", ObjectID: "p", Data: map[string]any{DataOrderID: order.String()}}, "/adv/orders/" + order.String()},
		{Notice{Template: "order.post_rejected", Role: RoleAdvertiser, ObjectType: "order", ObjectID: "o"}, "/adv/orders/o"},
		{Notice{Template: "balance.deposited", Role: RoleAdvertiser}, "/adv/balance"},
		{Notice{Template: "channel.approved", Role: RoleBlogger, ObjectType: "channel", ObjectID: "c"}, "/blg/channels/c"},
		{Notice{Template: "payout.succeeded", Role: RoleBlogger, ObjectType: "payout", ObjectID: "x"}, "/blg/payouts"},
		{Notice{Template: "placement.failed", Role: RoleAdmin, ObjectType: "placement", ObjectID: "p"}, "/adm/placements/p"},
	}
	for _, c := range cases {
		if got := Link(c.n); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.n.Template, c.n.Role, got, c.want)
		}
	}
}

func TestStateChanges(t *testing.T) {
	n := Notification{Channel: ChannelEmail, Status: StatusPending}
	if !n.Deliverable() {
		t.Fatal("pending email is deliverable")
	}
	if err := n.MarkRead(now); !errors.Is(err, ErrNotReadable) {
		t.Fatalf("email read: %v", err)
	}
	n.MarkAttempt("timeout", now)
	n.MarkFailed("rejected", now)
	if n.Status != StatusFailed || n.Attempts != 2 || n.Deliverable() {
		t.Fatalf("failed: %+v", n)
	}
	c := Notification{Channel: ChannelCabinet, Status: StatusSent}
	if c.Deliverable() {
		t.Fatal("cabinet is not delivered by job")
	}
	if err := c.MarkRead(now); err != nil || c.Status != StatusRead || c.ReadAt == nil {
		t.Fatalf("read: %v %+v", err, c)
	}
	if err := c.MarkRead(now.Add(time.Hour)); err != nil || !c.ReadAt.Equal(now) {
		t.Fatal("repeat read keeps first time")
	}
}

func TestPlanPersonalMAXBindingsForBothRoles(t *testing.T) {
	owner, member, unbound := uuid.New(), uuid.New(), uuid.New()
	legacy := int64(99)
	for _, tc := range []struct {
		role  Role
		event string
	}{{RoleAdvertiser, "balance.deposited"}, {RoleBlogger, "payout.succeeded"}} {
		t.Run(string(tc.role), func(t *testing.T) {
			a := Audience{UserIDs: []uuid.UUID{owner, member, unbound}, MaxUserID: &legacy, MAXUsers: map[uuid.UUID]int64{owner: 41, member: 42}}
			rows, err := Plan(Notice{Role: tc.role, Template: tc.event, OrganizationID: uuid.New()}, a, seqIDs(), now)
			if err != nil {
				t.Fatal(err)
			}
			counts := countBy(rows)
			if counts[ChannelMaxBot] != 2 || counts[ChannelCabinet] != 3 || counts[ChannelEmail] != 3 {
				t.Fatalf("channels %v", counts)
			}
			for _, row := range rows {
				bound, _ := row.Payload["_max_bot_binding"].(bool)
				if row.Channel == ChannelMaxBot {
					if !bound || row.MaxUserID == nil || *row.MaxUserID != a.MAXUsers[row.UserID] {
						t.Fatalf("wrong personal recipient %+v", row)
					}
				} else if bound {
					t.Fatal("personal binding marker leaked to email or cabinet")
				}
			}
		})
	}
}
