package server_test

import (
	"context"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/seed"
	"maxim/mockapi/internal/store"
)

func TestChannelMetricsHistoryIsObservedRatherThanSynthesized(t *testing.T) {
	e := newEnv(t)
	tx, done := e.st.Begin(context.Background())
	base, err := tx.Channel(seed.ChannelBlogger1)
	if err != nil {
		t.Fatal(err)
	}
	channel := *base
	channel.ID, channel.CreatedAt, channel.MetricsHistory = store.NewID(), tx.Now, nil
	channel.Subscribers, channel.AvgViews = 4, 5
	tx.D.Channels = append(tx.D.Channels, &channel)
	now := tx.Now.UTC()
	done()
	client := e.client(adv)
	for range 2 {
		r := e.do(client, "GET", "/catalog/channels/"+channel.ID.String(), nil, "")
		if r.status != 200 {
			t.Fatalf("new channel: %d %s", r.status, r.body)
		}
		var result api.CatalogChannelDetail
		r.json(t, &result)
		if len(result.MetricsHistory) != 0 || result.SubscribersCount != 4 || result.AvgViews == nil || *result.AvgViews != 5 {
			t.Fatalf("current parameters must not create history: %+v", result)
		}
	}
	msk := time.FixedZone("MSK", 3*60*60)
	y, m, d := now.In(msk).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	previous := now.Add(-24 * time.Hour)
	average, zero := 5, int64(0)
	tx, done = e.st.Begin(context.Background())
	channel.MetricsHistory = []api.ChannelMetricsPoint{
		{Date: openapi_types.Date{Time: yesterday}, SubscribersCount: 4, AvgViews: &average, ObservedAt: &previous},
		{Date: openapi_types.Date{Time: today}, SubscribersCount: 4, AvgViews: &average, NewViews: &zero, ObservedAt: &now, ViewsPeriodStartedAt: &previous},
	}
	done()
	r := e.do(client, "GET", "/catalog/channels/"+channel.ID.String(), nil, "")
	var result api.CatalogChannelDetail
	r.json(t, &result)
	if len(result.MetricsHistory) != 2 || result.MetricsHistory[0].NewViews != nil || result.MetricsHistory[1].NewViews == nil || *result.MetricsHistory[1].NewViews != 0 {
		t.Fatalf("unknown baseline and known zero must differ: %+v", result.MetricsHistory)
	}
	if result.MetricsHistory[0].SubscribersChange != nil || result.MetricsHistory[1].SubscribersChange == nil || *result.MetricsHistory[1].SubscribersChange != 0 {
		t.Fatalf("subscriber baseline and known zero must differ: %+v", result.MetricsHistory)
	}
	tx, done = e.st.Begin(context.Background())
	channel.MetricsHistory[1].SubscribersCount = 2
	done()
	r = e.do(client, "GET", "/catalog/channels/"+channel.ID.String(), nil, "")
	result = api.CatalogChannelDetail{}
	r.json(t, &result)
	if result.MetricsHistory[1].SubscribersChange == nil || *result.MetricsHistory[1].SubscribersChange != -2 {
		t.Fatalf("unsubscriptions must preserve signed net change: %+v", result.MetricsHistory)
	}
	tx, done = e.st.Begin(context.Background())
	channel.MetricsHistory[0].Date.Time = today.AddDate(0, 0, -3)
	done()
	r = e.do(client, "GET", "/catalog/channels/"+channel.ID.String(), nil, "")
	result = api.CatalogChannelDetail{}
	r.json(t, &result)
	if result.MetricsHistory[1].SubscribersChange != nil {
		t.Fatalf("gap must not be reported as a daily subscriber change: %+v", result.MetricsHistory)
	}
}
