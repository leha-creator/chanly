package domain

import (
	"context"
	"net/url"
	"time"

	"github.com/google/uuid"
)

type SubscriptionSpec struct {
	URI, Secret string
	Streams     []string
	RetryLimit  int
}

func (s SubscriptionSpec) Validate() error {
	u, err := url.Parse(s.URI)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || s.Secret == "" || s.RetryLimit < 1 || s.RetryLimit > 15 || len(s.Streams) == 0 {
		return ErrInvalidPayout
	}
	seen := map[string]bool{}
	for _, v := range s.Streams {
		if seen[v] || (v != "tasks" && v != "executors" && v != "company_accounts") {
			return ErrInvalidPayout
		}
		seen[v] = true
	}
	return nil
}

type OwnedSubscription struct {
	ID      int64
	Matches bool
}
type HistoryPage struct {
	Events               []WebhookEvent
	Current, Last, Total int
}
type EventProvider interface {
	Subscriptions(context.Context, SubscriptionSpec) ([]OwnedSubscription, error)
	CreateSubscription(context.Context, SubscriptionSpec) (int64, error)
	DeleteSubscription(context.Context, int64, SubscriptionSpec) error
	Postbacks(context.Context, time.Time, time.Time, int) (HistoryPage, error)
}
type EventCommand struct {
	ID                  uuid.UUID
	Scope               ProviderScope
	Action, Hash, State string
	SubscriptionID      int64
}
type EventStore interface {
	PrepareEventCommand(context.Context, EventCommand) (EventCommand, error)
	ClaimEventCommand(context.Context, uuid.UUID) (bool, error)
	ResolveEventCommand(context.Context, uuid.UUID, int64) error
	EventCursor(context.Context, ProviderScope) (time.Time, error)
	AdvanceEventCursor(context.Context, ProviderScope, time.Time) error
}
