package postgres

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Collector отдаёт остатки журнала по видам счетов (07 §5.5): maxim_ledger_balance_kopecks{kind}.
// Значения читаются из базы при сборе метрик.
type Collector struct {
	s       *Store
	balance *prometheus.Desc
}

// NewCollector создаёт сборщик остатков журнала.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s, balance: prometheus.NewDesc("maxim_ledger_balance_kopecks",
		"Ledger balances by account kind, kopecks.", []string{"kind"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.balance }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.s.BalancesByKind(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "ledger metrics query failed", logx.KeyOp, "Ledger.Metrics", logx.KeyError, err.Error())
		return
	}
	for kind, balance := range rows {
		ch <- prometheus.MustNewConstMetric(c.balance, prometheus.GaugeValue, float64(balance), string(kind))
	}
}

// Metrics — счётчики журнала (07 §5.5): проведённые транзакции и расхождения проверки
// сходимости. Реализует app.Observer.
type Metrics struct {
	transactions  *prometheus.CounterVec
	checkFailures *prometheus.CounterVec
}

// NewMetrics создаёт и регистрирует метрики в reg.
func NewMetrics(reg interface{ MustRegister(...prometheus.Collector) }) *Metrics {
	m := &Metrics{
		transactions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_ledger_transactions_total",
			Help: "Posted ledger transactions by kind."}, []string{"kind"}),
		checkFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "maxim_ledger_check_failures_total",
			Help: "Ledger balance check failures by check."}, []string{"check"}),
	}
	reg.MustRegister(m.transactions, m.checkFailures)
	return m
}

func (m *Metrics) Posted(kind domain.Kind)  { m.transactions.WithLabelValues(string(kind)).Inc() }
func (m *Metrics) CheckFailed(check string) { m.checkFailures.WithLabelValues(check).Inc() }
