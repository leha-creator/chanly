package seed

import (
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"maxim/mockapi/internal/api"
)

// demoMetrics is explicit seed data, not a reconstruction for newly connected
// channels. Reading a channel never creates or extends its historical records.
func demoMetrics(now time.Time, subscribers, average int) []api.ChannelMetricsPoint {
	out := make([]api.ChannelMetricsPoint, 0, 30)
	for i := 29; i >= 0; i-- {
		observed := now.AddDate(0, 0, -i).UTC()
		y, m, d := observed.In(time.FixedZone("MSK", 3*60*60)).Date()
		point := api.ChannelMetricsPoint{
			Date:             openapi_types.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)},
			SubscribersCount: subscribers - i*subscribers/200,
			AvgViews:         ptr(average - i*average/300), ObservedAt: &observed,
		}
		if i < 29 {
			gain := int64(average/4 + (i%3)*average/20)
			if i%7 == 0 {
				gain = 0
			}
			started := observed.Add(-24 * time.Hour)
			point.NewViews, point.ViewsPeriodStartedAt = &gain, &started
		}
		out = append(out, point)
	}
	return out
}
