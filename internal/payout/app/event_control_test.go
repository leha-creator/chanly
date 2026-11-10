package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/payout/domain"
	payoutjobs "github.com/leha-creator/chanly/internal/payout/jobs"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type eventProvider struct {
	domain.Provider
	writes   int
	visible  bool
	failPage bool
}

func TestEarlyTaskEventWaitsForBinding(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	log := logx.Discard()
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	recorder := inbound.NewRecorder(e.d.Pool, e.tx, job.NewQueue(e.d.Pool, e.clk, log), e.clk, log)
	dispatcher := inbound.NewDispatcher(e.d.Pool, e.clk, log)
	payoutjobs.RegisterEvents(dispatcher, e.svc)
	payload, _ := json.Marshal(app.EventPayload{ExternalID: "77", EventID: "19", ScopeKey: e.svc.WebhookScopeKey()})
	key := e.svc.WebhookScopeKey() + ":19"
	if _, err := recorder.Record(ctx, inbound.Event{Source: inbound.SourceRocketWork, ExternalKey: key, EventType: string(domain.WebhookPayoutStatus), Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := e.d.Pool.QueryRow(ctx, "SELECT id FROM inbound_event WHERE external_key=$1", key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"inbound_event_id": id})
	if err := dispatcher.Process(ctx, job.Running{Payload: raw}); !isPostponed(err) {
		t.Fatalf("early event discarded: %v", err)
	}
	var status string
	if err := e.d.Pool.QueryRow(ctx, "SELECT status FROM inbound_event WHERE id=$1", id).Scan(&status); err != nil || status != "received" {
		t.Fatal("early event not retained")
	}
	if duplicate, err := recorder.Record(ctx, inbound.Event{Source: inbound.SourceRocketWork, ExternalKey: key, EventType: string(domain.WebhookPayoutStatus), Payload: payload}); err != nil || !duplicate {
		t.Fatal("event replay not deduplicated")
	}
}

func (p *eventProvider) Subscriptions(context.Context, domain.SubscriptionSpec) ([]domain.OwnedSubscription, error) {
	if p.visible {
		return []domain.OwnedSubscription{{ID: 9, Matches: true}}, nil
	}
	return nil, nil
}
func (p *eventProvider) CreateSubscription(context.Context, domain.SubscriptionSpec) (int64, error) {
	p.writes++
	return 0, errors.New("response lost")
}
func (p *eventProvider) DeleteSubscription(context.Context, int64, domain.SubscriptionSpec) error {
	p.writes++
	return errors.New("response lost")
}
func (p *eventProvider) Postbacks(_ context.Context, _, _ time.Time, page int) (domain.HistoryPage, error) {
	if page == 2 && p.failPage {
		return domain.HistoryPage{}, errors.New("read failed")
	}
	id := "11"
	if page == 2 {
		id = "12"
	}
	return domain.HistoryPage{Current: page, Last: 2, Total: 2, Events: []domain.WebhookEvent{{Kind: domain.WebhookPayoutStatus, ExternalID: "7", EventID: id}}}, nil
}

type eventRecorder struct{ keys map[string]bool }

func (r eventRecorder) Record(_ context.Context, e inbound.Event) (bool, error) {
	dup := r.keys[e.ExternalKey]
	r.keys[e.ExternalKey] = true
	return dup, nil
}

func TestSubscriptionUnknownOutcomeNeverCreatesAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := &eventProvider{}
	e.svc.Provider = p
	e.svc.Config.ProviderScope = &domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	spec := domain.SubscriptionSpec{URI: "https://callback.example.test/rw", Secret: "private-test-secret", Streams: []string{"tasks"}, RetryLimit: 15}
	id := uuid.New()
	for i := 0; i < 2; i++ {
		if _, err := e.svc.ChangeSubscription(ctx, id, "create", 0, spec); !isPostponed(err) {
			t.Fatal(err)
		}
	}
	if p.writes != 1 {
		t.Fatal("uncertain subscription POST repeated")
	}
	p.visible = true
	got, err := e.svc.ChangeSubscription(ctx, id, "create", 0, spec)
	if err != nil || got != 9 {
		t.Fatal(err)
	}
	if _, err := e.svc.ChangeSubscription(ctx, id, "create", 0, spec); err != nil {
		t.Fatal(err)
	}
	var persisted string
	if err := e.d.Pool.QueryRow(ctx, "SELECT row_to_json(c)::text FROM rocketwork_event_command c WHERE id=$1", id).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, spec.Secret) || strings.Contains(persisted, spec.URI) {
		t.Fatal("subscription secret or URI persisted in checkpoint")
	}
}

func TestHistoryCursorAdvancesOnlyAfterAllPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := &eventProvider{failPage: true}
	e.svc.Provider = p
	scope := domain.ProviderScope{Environment: "local-emulator", Account: "synthetic"}
	e.svc.Config.ProviderScope = &scope
	recorder := eventRecorder{keys: map[string]bool{}}
	from, through := start.Add(-time.Hour), start
	if err := e.svc.RecoverPostbacks(ctx, recorder, from, through); err == nil {
		t.Fatal("partial history accepted")
	}
	store := e.svc.Store.(domain.EventStore)
	cursor, err := store.EventCursor(ctx, scope)
	if err != nil || !cursor.IsZero() {
		t.Fatal("cursor advanced past failed page")
	}
	p.failPage = false
	if err := e.svc.RecoverPostbacks(ctx, recorder, from, through); err != nil {
		t.Fatal(err)
	}
	if len(recorder.keys) != 2 {
		t.Fatal("replay did not deduplicate")
	}
	cursor, err = store.EventCursor(ctx, scope)
	if err != nil || !cursor.Equal(through) {
		t.Fatal("cursor not committed")
	}
	other := scope
	other.Account = "other-account"
	cursor, err = store.EventCursor(ctx, other)
	if err != nil || !cursor.IsZero() {
		t.Fatal("cross-account cursor")
	}
}
