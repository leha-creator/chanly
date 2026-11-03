package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	billingpg "github.com/leha-creator/chanly/internal/billing/postgres"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	markingpg "github.com/leha-creator/chanly/internal/marking/postgres"
	notificationpg "github.com/leha-creator/chanly/internal/notification/postgres"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
)

// runWorker — режим worker: обработчик заданий и метрики очереди.
func runWorker(ctx context.Context, cfg Config, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	reg := metrics.New()
	in, err := newInfra(ctx, cfg, ModeWorker, pool, log, reg)
	if err != nil {
		return err
	}
	if err := checkMAX(ctx, cfg, in); err != nil {
		return err
	}
	ensureMAXSubscription(ctx, cfg, in)
	in.alerter.CheckChannels(ctx)
	m := newModules(cfg, in)
	if err := m.startMarking(ctx); err != nil {
		return err
	}
	w := newWorker(in, m)
	reg.MustRegister(job.NewCollector(pool, in.clock, log.With(logx.KeyModule, "job")))
	reg.MustRegister(inbound.NewCollector(pool, in.clock, log.With(logx.KeyModule, "inbound")))
	reg.MustRegister(placementpg.NewCollector(m.placementStore))
	reg.MustRegister(markingpg.NewCollector(m.markingStore))
	reg.MustRegister(ledgerpg.NewCollector(m.ledgerStore))
	reg.MustRegister(billingpg.NewCollector(m.billingStore))
	reg.MustRegister(notificationpg.NewCollector(m.notificationStore))
	log.InfoContext(ctx, "worker configured", logx.KeyOp, "Bootstrap.Worker", "kinds", w.Kinds(),
		"max_mode", maxMode(cfg), "ord_enabled", cfg.ORD.Enabled, "ord_fake", cfg.ORDFake(), "alert_emails", len(cfg.Alert.Emails), "alert_chat", cfg.Alert.MAXChatID != 0)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return w.Run(gctx) })
	// /readyz worker — для HEALTHCHECK compose при выпуске без простоя (07 §8).
	ready := in.life.ready(readiness(pool))
	g.Go(func() error {
		return serve(gctx, log, stopPolicy{life: &in.life}, metricsServer(cfg.MetricsAddr, reg, log, ready))
	})
	return g.Wait()
}

// newWorker создаёт обработчик заданий со всеми видами заданий модулей.
func newWorker(in *infra, m *modules) *job.Worker {
	w := job.NewWorker(in.pool, in.clock, in.log.With(logx.KeyModule, "job"))
	m.registerJobs(w)
	return w
}

func maxMode(cfg Config) string {
	if cfg.MAXFake() {
		return "fake"
	}
	return "live"
}

// checkMAX проверяет токен бота при запуске в продакшене: без связи с MAX
// worker не стартует (07 §8).
func checkMAX(ctx context.Context, cfg Config, in *infra) error {
	if cfg.Env != EnvProduction {
		return nil
	}
	bot, err := in.max.GetMe(ctx)
	if err != nil {
		in.log.ErrorContext(ctx, "max bot check failed", logx.KeyOp, "Bootstrap.CheckMAX", logx.KeyError, err.Error())
		return fmt.Errorf("max bot check: %w", err)
	}
	in.log.InfoContext(ctx, "max bot checked", logx.KeyOp, "Bootstrap.CheckMAX", "bot_user_id", bot.UserID)
	return nil
}
