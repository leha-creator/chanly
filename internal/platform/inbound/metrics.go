package inbound

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/inbound/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Collector отдаёт возраст самого старого необработанного входящего события
// по источнику (07 §5.5). Значение читается из базы при сборе метрик.
type Collector struct {
	pool   *pgxpool.Pool
	clock  clock.Clock
	log    *slog.Logger
	oldest *prometheus.Desc
}

// NewCollector создаёт сборщик.
func NewCollector(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Collector {
	return &Collector{pool: pool, clock: clk, log: log,
		oldest: prometheus.NewDesc("maxim_inbound_event_oldest_unprocessed_seconds",
			"Age of the oldest inbound event still in received status.", []string{"source"}, nil)}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.oldest }

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := sqlc.New(c.pool).OldestReceivedInboundEvent(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "metrics collect failed", logx.KeyOp, "Inbound.Metrics", "collector", "inbound_event", logx.KeyError, err.Error())
		return
	}
	now := c.clock.Now()
	for _, r := range rows {
		ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, now.Sub(r.Oldest).Seconds(), r.Source)
	}
}
