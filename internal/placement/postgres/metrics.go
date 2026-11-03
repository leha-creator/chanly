package postgres

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Collector отдаёт число размещений по состояниям (07 §5.5): maxim_placements{status}.
// Значения читаются из базы при сборе метрик.
type Collector struct {
	s     *Store
	count *prometheus.Desc
}

// NewCollector создаёт сборщик метрик размещений.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s, count: prometheus.NewDesc("maxim_placements", "Placements by status.", []string{"status"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.count }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := c.s.Placements().CountByStatus(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "placement metrics query failed", logx.KeyOp, "Placement.Metrics", logx.KeyError, err.Error())
		return
	}
	for status, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.count, prometheus.GaugeValue, float64(n), string(status))
	}
}

// ControlMetrics — метрики публикации и контроля (07 §5.5, R-15): задержка публикации и снятия
// относительно плана, число нарушений по виду и источнику. Реализует app.Observer.
type ControlMetrics struct {
	publishDelay prometheus.Histogram
	removeDelay  prometheus.Histogram
	violations   *prometheus.CounterVec
	accepted     *prometheus.CounterVec
	opened       *prometheus.CounterVec
	resolved     *prometheus.CounterVec
	// resolution — время от открытия спора до решения (maxim_dispute_resolution_seconds, этап «API администратора»).
	resolution *prometheus.HistogramVec
}

// delayBuckets — от секунды до часа: требование точности — 10 с (07 §2).
var delayBuckets = []float64{1, 2, 5, 10, 30, 60, 300, 900, 1800, 3600}

// NewControlMetrics создаёт и регистрирует метрики в reg.
func NewControlMetrics(reg interface{ MustRegister(...prometheus.Collector) }) *ControlMetrics {
	m := &ControlMetrics{
		publishDelay: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "maxim_placement_publish_delay_seconds",
			Help: "Publication delay relative to publish_at.", Buckets: delayBuckets}),
		removeDelay: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "maxim_placement_remove_delay_seconds",
			Help: "Removal delay relative to remove_at.", Buckets: delayBuckets}),
		violations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_placement_violations_total",
			Help: "Placement violations found by control."}, []string{"kind", "source"}),
		accepted: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_placements_accepted_total",
			Help: "Placements accepted, by transition (P-12, P-13, P-15)."}, []string{"transition"}),
		opened: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_disputes_opened_total",
			Help: "Placement disputes opened."}, []string{"opened_by", "reason"}),
		resolved: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_disputes_resolved_total",
			Help: "Placement disputes resolved."}, []string{"resolution"}),
		resolution: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "maxim_dispute_resolution_seconds",
			Help: "Time from dispute opening to resolution.", Buckets: resolutionBuckets}, []string{"resolution"}),
	}
	reg.MustRegister(m.publishDelay, m.removeDelay, m.violations, m.accepted, m.opened, m.resolved, m.resolution)
	return m
}

func (m *ControlMetrics) PublishDelay(d time.Duration) { m.publishDelay.Observe(max(d.Seconds(), 0)) }
func (m *ControlMetrics) RemoveDelay(d time.Duration)  { m.removeDelay.Observe(max(d.Seconds(), 0)) }

func (m *ControlMetrics) Violation(kind domain.ViolationKind, source domain.ViolationSource) {
	m.violations.WithLabelValues(string(kind), string(source)).Inc()
}

func (m *ControlMetrics) Accepted(code domain.TransitionCode) {
	m.accepted.WithLabelValues(string(code)).Inc()
}

func (m *ControlMetrics) DisputeOpened(openedBy domain.OpenedBy, reason string) {
	m.opened.WithLabelValues(string(openedBy), reason).Inc()
}

func (m *ControlMetrics) DisputeResolved(res domain.Resolution, took time.Duration) {
	m.resolved.WithLabelValues(string(res)).Inc()
	m.resolution.WithLabelValues(string(res)).Observe(max(took.Seconds(), 0))
}

// resolutionBuckets — от часа до двух сроков T-06 (72 ч).
var resolutionBuckets = []float64{3600, 4 * 3600, 12 * 3600, 24 * 3600, 48 * 3600, 72 * 3600, 144 * 3600}
