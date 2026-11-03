package postgres

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Collector отдаёт число объектов ОРД по виду и состоянию (07 §5.5, «число объектов ОРД в
// error»): maxim_ord_objects{type,sync_status}. Значения читаются из базы при сборе метрик.
type Collector struct {
	s     *Store
	count *prometheus.Desc
}

// NewCollector создаёт сборщик метрик объектов ОРД.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s, count: prometheus.NewDesc("maxim_ord_objects", "ORD objects by type and sync status.",
		[]string{"type", "sync_status"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.count }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := c.s.Overview().CountByStatus(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "ord metrics query failed", logx.KeyOp, "Marking.Metrics", logx.KeyError, err.Error())
		return
	}
	for t, byStatus := range counts {
		for st, n := range byStatus {
			ch <- prometheus.MustNewConstMetric(c.count, prometheus.GaugeValue, float64(n), string(t), string(st))
		}
	}
}

// EridMetrics — время от одобрения версии до erid (07 §5.5): maxim_ord_erid_wait_seconds.
// Реализует app.Observer.
type EridMetrics struct {
	wait prometheus.Histogram
}

// eridBuckets — от секунд до суток: T-11 — час, статус ОРД ждём до 24 ч.
var eridBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600, 86400}

// NewEridMetrics создаёт и регистрирует метрику в reg.
func NewEridMetrics(reg interface{ MustRegister(...prometheus.Collector) }) *EridMetrics {
	m := &EridMetrics{wait: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "maxim_ord_erid_wait_seconds",
		Help: "Time from post version approval to erid.", Buckets: eridBuckets})}
	reg.MustRegister(m.wait)
	return m
}

func (m *EridMetrics) EridWait(d time.Duration) { m.wait.Observe(max(d.Seconds(), 0)) }
