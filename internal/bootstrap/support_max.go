package bootstrap

import (
	"context"
	"errors"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func (in *infra) connectSupportMAX(cfg Config) error {
	if !cfg.SupportMAX.Enabled || cfg.SupportFake() {
		return nil
	}
	httpClient, err := max.NewHTTPClient(cfg.MAX.CACertFile)
	if err != nil {
		return err
	}
	log := in.log.With("module", "support_max")
	outMetrics, limitMetrics := in.integrationMetrics()
	out := outbound.New(in.pool, in.clock, log, outbound.Options{System: "max_support", HTTP: outbound.PublicMediaClient(httpClient), Alerter: in.alerter, Metrics: outMetrics})
	limiter := ratelimit.New(in.pool, in.clock, log, ratelimit.Options{Metrics: limitMetrics})
	in.supportMAX = max.New(out, limiter, max.Options{BaseURL: cfg.MAX.APIURL, Token: cfg.SupportMAX.BotToken, BotUserID: cfg.SupportMAX.BotUserID, GlobalRate: cfg.MAX.RateGlobal, ChatRate: cfg.MAX.RatePerChat, Namespace: "max_support", MediaHosts: cfg.SupportMAX.MediaHosts}, log)
	return nil
}

func ensureSupportSubscription(ctx context.Context, cfg Config, in *infra) error {
	if in.supportMAX == nil {
		return nil
	}
	bot, err := in.supportMAX.GetMe(ctx)
	if err != nil {
		return errors.New("support bot identity check failed")
	}
	if bot.UserID != cfg.SupportMAX.BotUserID {
		return errors.New("support bot identity differs from configured ID")
	}
	if !cfg.MAXFake() {
		main, e := in.max.GetMe(ctx)
		if e != nil {
			return errors.New("primary bot identity check failed")
		}
		if main.UserID == bot.UserID {
			return errors.New("support bot must differ from primary bot")
		}
	}
	subscriptions, err := in.supportMAX.ListSubscriptions(ctx)
	if err != nil {
		return errors.New("support subscription check failed")
	}
	target := cfg.SupportMAX.WebhookURL + "/" + cfg.SupportMAX.WebhookPathSecret
	for _, s := range subscriptions {
		if s.URL != target {
			return errors.New("support bot has another webhook; preserve it and resolve configuration")
		}
	}
	if err = in.supportMAX.Subscribe(ctx, target, []string{"bot_started", "message_created", "message_edited", "message_removed"}, cfg.SupportMAX.WebhookSecret); err != nil {
		return errors.New("support subscription configuration failed")
	}
	in.log.InfoContext(ctx, "support bot configured", "bot_user_id", bot.UserID)
	return nil
}
