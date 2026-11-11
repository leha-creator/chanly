package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/google/uuid"
)

type StreamEvent struct {
	Name string
	Data any
}

type Invalidation struct {
	Version    int        `json:"version"`
	Topic      string     `json:"topic"`
	ObjectID   *uuid.UUID `json:"object_id,omitempty"`
	OrderID    *uuid.UUID `json:"order_id,omitempty"`
	CampaignID *uuid.UUID `json:"campaign_id,omitempty"`
	ChannelID  *uuid.UUID `json:"channel_id,omitempty"`
}

type Stream struct {
	Events   chan StreamEvent
	Done     chan struct{}
	s        *Store
	token    string
	admin    bool
	identity streamIdentity
}

type streamIdentity struct {
	User                uuid.UUID
	Org                 *uuid.UUID
	Role                string
	Advertiser, Blogger bool
}

func (s *Store) streamIdentity(token string, admin bool) (streamIdentity, error) {
	sess, ok := s.sessions[token]
	if !ok || !s.clock.Now().Before(sess.ExpiresAt) {
		return streamIdentity{}, Unauthorized("session is missing")
	}
	u := s.data.Users[sess.UserID]
	if u == nil || u.Blocked {
		return streamIdentity{}, Unauthorized("session is invalid")
	}
	if admin && u.AdminRole != "moderator" && u.AdminRole != "senior" {
		return streamIdentity{}, Forbidden("admin_required")
	}
	id := streamIdentity{User: u.ID, Role: u.AdminRole}
	if u.OrgID != nil {
		org := s.data.Orgs[*u.OrgID]
		if org == nil || org.Status == "blocked" {
			return streamIdentity{}, Unauthorized("organization access changed")
		}
		orgID := *u.OrgID
		id.Org = &orgID
		id.Advertiser = org.IsAdvertiser
		id.Blogger = org.IsBlogger
	}
	return id, nil
}

func (s *Store) Subscribe(token string, admin bool) (*Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.streamIdentity(token, admin)
	if err != nil {
		return nil, err
	}
	st := &Stream{s: s, token: token, admin: admin, identity: id, Events: make(chan StreamEvent, 128), Done: make(chan struct{})}
	if s.streams == nil {
		s.streams = make(map[*Stream]struct{})
	}
	s.streams[st] = struct{}{}
	return st, nil
}

func (st *Stream) Valid() bool {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	id, err := st.s.streamIdentity(st.token, st.admin)
	return err == nil && reflect.DeepEqual(id, st.identity)
}

func (s *Store) closeStream(st *Stream) {
	if _, ok := s.streams[st]; ok {
		delete(s.streams, st)
		close(st.Done)
	}
}

func (st *Stream) Close() { st.s.mu.Lock(); defer st.s.mu.Unlock(); st.s.closeStream(st) }

func (s *Store) CloseStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for st := range s.streams {
		s.closeStream(st)
	}
}

func (s *Store) resync() {
	for st := range s.streams {
		select {
		case st.Events <- StreamEvent{Name: "resync_required", Data: map[string]int{"version": 1}}:
		default:
			s.closeStream(st)
		}
	}
}

// Resync is called under Tx's lock by deterministic mock clock controls.
func (tx *Tx) Resync() { tx.s.resync() }

type streamSnapshot struct {
	Scope    string
	Audience uuid.UUID
	Event    Invalidation
	Hash     [32]byte
}

func (s *Store) realtimeSnapshot() map[string]streamSnapshot {
	out := make(map[string]streamSnapshot)
	add := func(scope string, aud uuid.UUID, topic string, obj, ord, camp, ch *uuid.UUID, source any, key string) {
		b, _ := json.Marshal(source)
		e := Invalidation{Version: 1, Topic: topic, ObjectID: obj, OrderID: ord, CampaignID: camp, ChannelID: ch}
		out[scope+aud.String()+topic+key] = streamSnapshot{Scope: scope, Audience: aud, Event: e, Hash: sha256.Sum256(b)}
	}
	org := func(aud uuid.UUID, topic string, id uuid.UUID, source any) {
		add("organization", aud, topic, &id, nil, nil, nil, source, id.String())
	}
	admin := func(topic string, source any) { add("admin", uuid.Nil, topic, nil, nil, nil, nil, source, "") }
	d := s.data
	for id, u := range d.Users {
		add("user", id, "session.changed", nil, nil, nil, nil, u, "")
	}
	for id, o := range d.Orgs {
		org(id, "organization.changed", id, o)
	}
	for _, n := range d.Notifications {
		add("user", n.UserID, "notification.changed", &n.ID, nil, nil, nil, n, n.ID.String())
	}
	for _, c := range d.Campaigns {
		add("organization", c.OrgID, "campaign.changed", &c.ID, c.OrderID, &c.ID, nil, c, c.ID.String())
		add("organization", c.OrgID, "post.changed", nil, c.OrderID, &c.ID, nil, c.Post, c.ID.String())
	}
	for _, o := range d.Orders {
		add("organization", o.OrgID, "order.changed", &o.ID, &o.ID, &o.CampaignID, nil, o, o.ID.String())
	}
	for _, p := range d.Placements {
		var o *Order
		for _, v := range d.Orders {
			if v.ID == p.OrderID {
				o = v
				break
			}
		}
		if o == nil {
			continue
		}
		add("organization", o.OrgID, "placement.changed", &p.ID, &o.ID, &o.CampaignID, &p.ChannelID, p, p.ID.String())
		add("organization", o.OrgID, "order.changed", &o.ID, &o.ID, &o.CampaignID, nil, p, p.ID.String())
		if p.Status != "created" {
			add("organization", p.BloggerOrgID, "placement.changed", &p.ID, &o.ID, &o.CampaignID, &p.ChannelID, p, p.ID.String())
		}
	}
	for _, c := range d.Channels {
		if c.OrgID != nil {
			add("organization", *c.OrgID, "channel.changed", &c.ID, nil, nil, &c.ID, c, c.ID.String())
		}
		if c.Status == "active" {
			add("catalog", uuid.Nil, "channel.changed", nil, nil, nil, nil, c, c.ID.String())
		}
	}
	for _, c := range d.ChannelConnections {
		add("user", c.UserID, "channel.changed", nil, nil, nil, nil, c, c.ID.String())
	}
	for _, c := range d.CartItems {
		org(c.OrgID, "cart.changed", c.ID, c)
	}
	for _, m := range d.Media {
		org(m.OrgID, "media.changed", m.ID, m)
	}
	for _, i := range d.Invoices {
		org(i.OrgID, "invoice.changed", i.ID, i)
	}
	for _, l := range d.Ledger {
		org(l.OrgID, "balance.changed", l.ID, l)
	}
	for _, a := range d.ClosingActs {
		org(a.OrgID, "closing_act.changed", a.ID, a)
	}
	for _, p := range d.Payouts {
		org(p.OrgID, "payout.changed", p.ID, p)
		org(p.OrgID, "balance.changed", p.ID, p)
	}
	for id, rw := range d.Rocketwork {
		org(id, "rocketwork.changed", id, rw)
	}
	add("reference", uuid.Nil, "category.changed", nil, nil, nil, nil, d.Categories, "")
	add("reference", uuid.Nil, "format.changed", nil, nil, nil, nil, d.Formats, "")
	admin("admin.channel.changed", d.Channels)
	admin("admin.order.changed", d.Orders)
	admin("admin.placement.changed", d.Placements)
	admin("admin.dispute.changed", d.DisputeMessages)
	admin("admin.organization.changed", d.Orgs)
	admin("admin.ord.changed", []any{d.OrdObjects, d.OrdReports})
	admin("admin.finance.changed", []any{d.Invoices, d.Ledger, d.Payouts, d.Approvals, d.AdminLedger, d.BankStatements, d.BankTxs, d.Rocketwork})
	admin("admin.audit.changed", d.Audit)
	admin("admin.counters.changed", []any{d.Channels, d.Orders, d.Placements, d.Invoices, d.Approvals, d.OrdObjects})
	return out
}

func (s *Store) publishChanges(before map[string]streamSnapshot) {
	after := s.realtimeSnapshot()
	keys := make([]string, 0)
	for k, v := range after {
		if prev, ok := before[k]; !ok || prev.Hash != v.Hash {
			keys = append(keys, k)
		}
	}
	for k, v := range before {
		if _, ok := after[k]; !ok {
			keys = append(keys, k)
			after[k] = v
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := after[k]
		for st := range s.streams {
			allowed := false
			switch e.Scope {
			case "user":
				allowed = e.Audience == st.identity.User && (!st.admin || e.Event.Topic == "session.changed" || e.Event.Topic == "notification.changed")
			case "organization":
				allowed = !st.admin && st.identity.Org != nil && *st.identity.Org == e.Audience
			case "catalog":
				allowed = !st.admin && st.identity.Advertiser
			case "reference":
				allowed = true
			case "admin":
				allowed = st.admin && ((e.Event.Topic != "admin.finance.changed" && e.Event.Topic != "admin.audit.changed") || st.identity.Role == "senior")
			}
			if allowed {
				select {
				case st.Events <- StreamEvent{Name: "invalidate", Data: e.Event}:
				default:
					s.closeStream(st)
				}
			}
		}
	}
	if len(keys) > 0 {
		s.log.DebugContext(context.Background(), "mock realtime changes", "count", len(keys))
	}
}
