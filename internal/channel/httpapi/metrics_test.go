package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/channel/domain"
)

func TestMetricsHistoryDistinguishesUnknownAndZero(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	zero := int64(0)
	negative := int64(-7)
	points := metricsHistory([]domain.MetricsSnapshot{
		{Date: at, SubscribersCount: 1201, ObservedAt: &at},
		{Date: at.AddDate(0, 0, 1), SubscribersCount: 1202, SubscribersChange: &zero, NewViews: &zero, ObservedAt: &at, ViewsPeriodStartedAt: &at},
		{Date: at.AddDate(0, 0, 2), SubscribersCount: 1195, SubscribersChange: &negative},
	})
	b, err := json.Marshal(points)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if _, exists := out[0]["new_views"]; exists {
		t.Fatalf("baseline exposes fabricated zero: %s", b)
	}
	if string(out[1]["new_views"]) != "0" {
		t.Fatalf("known zero omitted: %s", b)
	}
	if _, exists := out[0]["subscribers_change"]; exists {
		t.Fatalf("baseline exposes fabricated subscriber change: %s", b)
	}
	if string(out[1]["subscribers_change"]) != "0" || string(out[2]["subscribers_change"]) != "-7" {
		t.Fatalf("known zero or signed subscriber change lost: %s", b)
	}
	if _, exists := out[1]["views_period_started_at"]; !exists {
		t.Fatalf("actual period omitted: %s", b)
	}
}
