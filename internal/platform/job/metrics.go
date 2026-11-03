package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// overdueAfter — задание в pending, чьё время прошло больше чем на минуту, считается просроченным.
const overdueAfter = time.Minute

// Collector отдаёт метрики очереди по видам заданий (07 §5.5): в очереди, в
// работе, просроченные, сбойные. Значения читаются из базы при сборе метрик.
type Collector struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	log   *slog.Logger
	jobs  *prometheus.Desc
	late  *prometheus.Desc
	lag   *prometheus.Desc
}

// NewCollector создаёт сборщик метрик очереди.
func NewCollector(pool *pgxpool.Pool, clk clock.Clock, log *slog.Logger) *Collector {
	return &Collector{
		pool:  pool,
		clock: clk,
		log:   log,
		jobs: prometheus.NewDesc("maxim_jobs", "Jobs by kind and status (pending, running, failed).",
			[]string{"kind", "status"}, nil),
		late: prometheus.NewDesc("maxim_jobs_overdue", "Pending jobs whose run_at passed more than a minute ago.",
			[]string{"kind"}, nil),
		lag: prometheus.NewDesc("maxim_jobs_lag_seconds",
			"Age of the oldest ready but not yet taken job by kind: queue delay against plan (07 §5.5).",
			[]string{"kind"}, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.jobs
	ch <- c.late
	ch <- c.lag
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q := sqlc.New(c.pool)
	counts, err := q.JobCounts(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "job metrics query failed", logx.KeyOp, "Job.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range counts {
		ch <- prometheus.MustNewConstMetric(c.jobs, prometheus.GaugeValue, float64(r.Count), r.Kind, r.Status)
	}
	late, err := q.OverdueJobCounts(ctx, c.clock.Now().Add(-overdueAfter))
	if err != nil {
		c.log.WarnContext(ctx, "job metrics query failed", logx.KeyOp, "Job.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range late {
		ch <- prometheus.MustNewConstMetric(c.late, prometheus.GaugeValue, float64(r.Count), r.Kind)
	}
	now := c.clock.Now()
	ready, err := q.OldestReadyJobs(ctx, now)
	if err != nil {
		c.log.WarnContext(ctx, "job metrics query failed", logx.KeyOp, "Job.Metrics", logx.KeyError, err.Error())
		return
	}
	for _, r := range ready {
		ch <- prometheus.MustNewConstMetric(c.lag, prometheus.GaugeValue, now.Sub(r.OldestRunAt).Seconds(), r.Kind)
	}
}
