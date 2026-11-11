// Package realtime delivers committed, payload-free cache invalidations.
package realtime

import (
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/httpx"
)

// Event is the complete SSE v1 invalidate payload. Audience never goes on the wire.
type Event struct {
	Version    int        `json:"version"`
	Topic      string     `json:"topic"`
	ObjectID   *uuid.UUID `json:"object_id,omitempty"`
	OrderID    *uuid.UUID `json:"order_id,omitempty"`
	CampaignID *uuid.UUID `json:"campaign_id,omitempty"`
	ChannelID  *uuid.UUID `json:"channel_id,omitempty"`
}

var topicPattern = regexp.MustCompile(`^[a-z][a-z_.]{0,63}$`)

type Record struct {
	ID          int64
	Transaction uint64
	Scope       string
	AudienceID  *uuid.UUID
	Event       Event
}

func (r Record) allowed(p *httpx.Principal, admin bool) bool {
	if p == nil || !topicPattern.MatchString(r.Event.Topic) || r.Event.Version != 1 {
		return false
	}
	if strings.HasPrefix(r.Event.Topic, "admin.") && !admin {
		return false
	}
	if admin && !slices.Contains(httpx.AdminAny, p.AdminRole) {
		return false
	}
	if slices.Contains([]string{"admin.finance.changed", "admin.audit.changed"}, r.Event.Topic) && !httpx.IsSenior(p) {
		return false
	}
	switch r.Scope {
	case "user":
		return r.AudienceID != nil && *r.AudienceID == p.UserID &&
			(!admin || r.Event.Topic == "session.changed" || r.Event.Topic == "notification.changed")
	case "organization":
		return !admin && p.OrganizationID != nil && r.AudienceID != nil && *p.OrganizationID == *r.AudienceID
	case "admin":
		return admin
	case "senior":
		return admin && httpx.IsSenior(p)
	case "catalog":
		return !admin && slices.Contains(p.Cabinets, "advertiser")
	case "reference":
		return true
	default:
		return false
	}
}
