package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/inbound"
)

// ChangeSubscription is an explicit command, never called during startup.
// A checkpoint claimed before the write survives timeouts and process crashes.
// Re-running the same command performs list reconciliation only.
func (s *Service) ChangeSubscription(ctx context.Context, commandID uuid.UUID, action string, id int64, spec domain.SubscriptionSpec) (int64, error) {
	store, ok := s.Store.(domain.EventStore)
	api, capable := s.Provider.(domain.EventProvider)
	if !ok || !capable || !s.Config.ProviderEnabled || s.Config.ProviderScope == nil {
		return 0, domain.ErrProviderDisabled
	}
	if commandID == uuid.Nil || (action != "create" && action != "delete") || (action == "delete" && id <= 0) || (action == "create" && id != 0) {
		return 0, domain.ErrInvalidPayout
	}
	if err := spec.Validate(); err != nil {
		return 0, err
	}
	spec.Streams = append([]string(nil), spec.Streams...)
	sort.Strings(spec.Streams)
	raw, _ := json.Marshal(struct {
		ID   int64
		Spec domain.SubscriptionSpec
	}{id, spec})
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	c, err := store.PrepareEventCommand(ctx, domain.EventCommand{ID: commandID, Scope: *s.Config.ProviderScope, Action: action, Hash: hash, SubscriptionID: id})
	if err != nil {
		return 0, err
	}
	if c.State == "confirmed" {
		return c.SubscriptionID, nil
	}
	rows, err := api.Subscriptions(ctx, spec)
	if err != nil {
		return 0, err
	}
	matched, exists := int64(0), false
	for _, row := range rows {
		if row.Matches {
			if matched != 0 {
				return 0, domain.ErrInvalidPayout
			}
			matched = row.ID
		}
		if row.ID == id {
			exists = true
			if !row.Matches && action == "delete" {
				return 0, domain.ErrInvalidPayout
			}
		}
	}
	if (action == "create" && matched != 0) || (action == "delete" && !exists) {
		if action == "delete" {
			matched = id
		}
		return matched, store.ResolveEventCommand(ctx, c.ID, matched)
	}
	if c.State != "prepared" {
		return 0, s.workflowWait("subscription outcome requires reconciliation")
	}
	claimed, err := store.ClaimEventCommand(ctx, c.ID)
	if err != nil {
		return 0, err
	}
	if !claimed {
		return 0, s.workflowWait("subscription command claimed")
	}
	s.log.InfoContext(ctx, "rocketwork subscription command started", "command_id", c.ID, "action", action)
	if action == "create" {
		id, err = api.CreateSubscription(ctx, spec)
	} else {
		err = api.DeleteSubscription(ctx, id, spec)
	}
	if err != nil {
		return 0, s.workflowWait("subscription write not confirmed")
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return id, store.ResolveEventCommand(saveCtx, c.ID, id)
}

type EventRecorder interface {
	Record(context.Context, inbound.Event) (bool, error)
}

// RecoverPostbacks uses a closed interval and an overlapping boundary. A crash
// before cursor commit re-ingests the same event IDs without repeating effects.
// An explicit earlier from allows recovery of arbitrarily delayed history.
func (s *Service) RecoverPostbacks(ctx context.Context, recorder EventRecorder, from, through time.Time) error {
	store, ok := s.Store.(domain.EventStore)
	api, capable := s.Provider.(domain.EventProvider)
	if !ok || !capable || s.Config.ProviderScope == nil || recorder == nil {
		return domain.ErrProviderDisabled
	}
	if through.IsZero() || through.After(s.Clock.Now()) {
		return domain.ErrInvalidPayout
	}
	if from.IsZero() {
		cursor, err := store.EventCursor(ctx, *s.Config.ProviderScope)
		if err != nil {
			return err
		}
		if cursor.IsZero() {
			return domain.ErrInvalidPayout
		}
		from = cursor.Add(-5 * time.Minute)
	}
	if through.Before(from) {
		return domain.ErrInvalidPayout
	}
	total, last, count := -1, -1, 0
	seen := map[string]bool{}
	for page := 1; page <= 10000; page++ {
		r, err := api.Postbacks(ctx, from, through, page)
		if err != nil {
			return err
		}
		if r.Current != page || r.Last < page || r.Total < len(r.Events) {
			return domain.ErrWebhookFormat
		}
		if page == 1 {
			total, last = r.Total, r.Last
		} else if total != r.Total || last != r.Last {
			return s.workflowWait("postback history changed; restart interval")
		}
		for _, e := range r.Events {
			if e.EventID == "" || seen[e.EventID] {
				return domain.ErrWebhookFormat
			}
			seen[e.EventID] = true
			count++
			payload, _ := json.Marshal(EventPayload{ExternalID: e.ExternalID, EventID: e.EventID, ScopeKey: s.WebhookScopeKey()})
			if _, err := recorder.Record(ctx, inbound.Event{Source: inbound.SourceRocketWork, ExternalKey: s.WebhookScopeKey() + ":" + e.EventID, EventType: string(e.Kind), Payload: payload}); err != nil {
				return err
			}
		}
		if page == last {
			if count != total {
				return domain.ErrWebhookFormat
			}
			if err := store.AdvanceEventCursor(ctx, *s.Config.ProviderScope, through); err != nil {
				return err
			}
			s.log.InfoContext(ctx, "rocketwork postback history ingested", "events", count, "through", through)
			return nil
		}
	}
	return domain.ErrWebhookFormat
}
