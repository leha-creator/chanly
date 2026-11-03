package postgres

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Collector отдаёт число поступлений по результату сопоставления и счетов по состояниям
// (07 §5.5): maxim_bank_transactions{match_status}, maxim_invoices{status}. Значения читаются
// из базы при сборе метрик.
type Collector struct {
	s        *Store
	bankTx   *prometheus.Desc
	invoices *prometheus.Desc
}

// NewCollector создаёт сборщик метрик расчётов.
func NewCollector(s *Store) *Collector {
	return &Collector{s: s,
		bankTx:   prometheus.NewDesc("maxim_bank_transactions", "Bank transactions by match status.", []string{"match_status"}, nil),
		invoices: prometheus.NewDesc("maxim_invoices", "Invoices by status.", []string{"status"}, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bankTx
	ch <- c.invoices
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bank, err := c.s.q(ctx).CountBankTransactionsByStatus(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "billing metrics query failed", logx.KeyOp, "Billing.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range bank {
		ch <- prometheus.MustNewConstMetric(c.bankTx, prometheus.GaugeValue, float64(r.N), r.MatchStatus)
	}
	inv, err := c.s.q(ctx).CountInvoicesByStatus(ctx)
	if err != nil {
		c.s.log.WarnContext(ctx, "billing metrics query failed", logx.KeyOp, "Billing.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range inv {
		ch <- prometheus.MustNewConstMetric(c.invoices, prometheus.GaugeValue, float64(r.N), r.Status)
	}
}
