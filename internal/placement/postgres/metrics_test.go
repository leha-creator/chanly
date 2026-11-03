package postgres

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/leha-creator/chanly/internal/placement/domain"
)

func TestControlMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewControlMetrics(reg)
	m.PublishDelay(3 * time.Second)
	m.RemoveDelay(-time.Second) // раньше плана — ноль
	m.Violation(domain.ViolationTopViolated, domain.SourceEvent)
	m.Violation(domain.ViolationTopViolated, domain.SourceEvent)
	if n := testutil.ToFloat64(m.violations.WithLabelValues("top_violated", "event")); n != 2 {
		t.Fatalf("violations %v", n)
	}
	if n := testutil.CollectAndCount(reg, "maxim_placement_publish_delay_seconds", "maxim_placement_remove_delay_seconds"); n != 2 {
		t.Fatalf("delay metrics %d", n)
	}
}
