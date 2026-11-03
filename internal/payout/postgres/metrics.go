package postgres

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Collector отдаёт число исполнителей Рокет Ворк по состояниям (07 §5.5):
// maxim_rocketwork_contractors{status}. Значения читаются из базы при сборе.
type Collector struct {
	s     *Store
	count *prometheus.Desc
}

// NewCollector создаёт сборщик.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s, count: prometheus.NewDesc("maxim_rocketwork_contractors",
		"Rocket Work contractors by status.", []string{"status"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.count }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := c.s.ContractorCounts(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "payout metrics query failed", logx.KeyOp, "Payout.Metrics", logx.KeyError, err.Error())
		return
	}
	for status, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.count, prometheus.GaugeValue, float64(n), string(status))
	}
}

// Metrics — метрики выплат (07 §5.5). Реализует app.Observer.
type Metrics struct {
	payouts *prometheus.CounterVec
	amount  *prometheus.CounterVec
	deposit *prometheus.GaugeVec
}

// NewMetrics создаёт и регистрирует метрики в reg.
func NewMetrics(reg interface{ MustRegister(...prometheus.Collector) }) *Metrics {
	m := &Metrics{
		payouts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_payouts_total",
			Help: "Finished payout attempts by status and method."}, []string{"status", "method"}),
		amount: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_payout_amount_kopecks_total",
			Help: "Amount of succeeded payouts in kopecks by method."}, []string{"method"}),
		deposit: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "maxim_rocketwork_deposit_kopecks",
			Help: "Rocket Work deposit balance by source (api, ledger)."}, []string{"source"}),
	}
	reg.MustRegister(m.payouts, m.amount, m.deposit)
	return m
}

func (m *Metrics) PayoutFinished(status domain.Status, method domain.Method, amount money.Kopecks) {
	m.payouts.WithLabelValues(string(status), string(method)).Inc()
	if status == domain.StatusSucceeded {
		m.amount.WithLabelValues(string(method)).Add(float64(amount))
	}
}

func (m *Metrics) Deposit(source string, v money.Kopecks) {
	m.deposit.WithLabelValues(source).Set(float64(v))
}
