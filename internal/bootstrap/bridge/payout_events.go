package bridge

import (
	"context"
	"time"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/payout/domain"
)

type eventAPI interface {
	ListSubscriptions(context.Context) ([]rocketwork.Subscription, error)
	CreateSubscription(context.Context, rocketwork.SubscriptionRequest) (rocketwork.Subscription, error)
	DeleteSubscription(context.Context, int64, rocketwork.SubscriptionRequest) error
	PostbackHistory(context.Context, time.Time, time.Time, int) (rocketwork.HistoryPage, error)
}

func subscriptionRequest(s domain.SubscriptionSpec) rocketwork.SubscriptionRequest {
	return rocketwork.SubscriptionRequest{URI: s.URI, Method: "post", Authorization: rocketwork.SubscriptionAuthorization{Type: "Bearer", Value: s.Secret}, Streams: s.Streams, RetryLimit: s.RetryLimit}
}
func (p PayoutProvider) Subscriptions(ctx context.Context, spec domain.SubscriptionSpec) ([]domain.OwnedSubscription, error) {
	api, ok := p.API.(eventAPI)
	if !ok {
		return nil, domain.ErrProviderDisabled
	}
	rows, err := api.ListSubscriptions(ctx)
	if err != nil {
		return nil, providerErr(err)
	}
	result := make([]domain.OwnedSubscription, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.OwnedSubscription{ID: row.ID, Matches: row.Matches(subscriptionRequest(spec))})
	}
	return result, nil
}
func (p PayoutProvider) CreateSubscription(ctx context.Context, spec domain.SubscriptionSpec) (int64, error) {
	api, ok := p.API.(eventAPI)
	if !ok {
		return 0, domain.ErrProviderDisabled
	}
	r, err := api.CreateSubscription(ctx, subscriptionRequest(spec))
	return r.ID, providerErr(err)
}
func (p PayoutProvider) DeleteSubscription(ctx context.Context, id int64, spec domain.SubscriptionSpec) error {
	api, ok := p.API.(eventAPI)
	if !ok {
		return domain.ErrProviderDisabled
	}
	return providerErr(api.DeleteSubscription(ctx, id, subscriptionRequest(spec)))
}
func (p PayoutProvider) Postbacks(ctx context.Context, from, through time.Time, page int) (domain.HistoryPage, error) {
	api, ok := p.API.(eventAPI)
	if !ok {
		return domain.HistoryPage{}, domain.ErrProviderDisabled
	}
	r, err := api.PostbackHistory(ctx, from, through, page)
	if err != nil {
		return domain.HistoryPage{}, providerErr(err)
	}
	out := domain.HistoryPage{Current: r.Current, Last: r.Last, Total: r.Total}
	for _, e := range r.Events {
		out.Events = append(out.Events, domain.WebhookEvent{Kind: domain.WebhookKind(e.Kind), ExternalID: e.ObjectID, EventID: e.EventID})
	}
	return out, nil
}
