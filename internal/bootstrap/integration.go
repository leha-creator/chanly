package bootstrap

import (
	"context"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/integration/max/maxfake"
	"github.com/leha-creator/chanly/internal/integration/ord"
	"github.com/leha-creator/chanly/internal/integration/ord/ordfake"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworkfake"
	notificationdomain "github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/cleanup"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/stuck"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// mailer — почта: письма с кодом входа и оповещения (integration/mail, mailfake).
type mailer interface {
	accountdomain.LoginCodeSender
	alert.Mailer
	notificationdomain.Mailer
}

// maxAPI — методы адаптера MAX, которые использует сборка (integration/max, maxfake).
type maxAPI interface {
	GetMe(ctx context.Context) (max.Bot, error)
	SendChatMessage(ctx context.Context, chatID int64, text string) (string, error)
	SendUserMessage(ctx context.Context, userID int64, text string) (string, error)
	GetChat(ctx context.Context, chatID int64) (max.Chat, error)
	GetMembership(ctx context.Context, chatID int64) (max.Membership, error)
	ListMessages(ctx context.Context, chatID int64, from, to time.Time, count int) ([]max.ChannelMessage, error)
	DeleteMessage(ctx context.Context, chatID int64, messageID string) error
	LeaveChat(ctx context.Context, chatID int64) error
	ListSubscriptions(ctx context.Context) ([]max.Subscription, error)
	Subscribe(ctx context.Context, url string, updateTypes []string, secret string) error
	// Публикация и контроль размещений (этап «Автопостинг и контроль»).
	bridge.MAXPosts
}

// newAlerter — оповещения администратору (07 §3, R-4, R-5).
func newAlerter(cfg Config, in *infra) *alert.Alerter {
	log := in.log.With(logx.KeyModule, "alert")
	return alert.New(in.pool, tx.NewRunner(in.pool, log), job.NewQueue(in.pool, in.clock, log), in.clock, log,
		alert.Options{Emails: cfg.Alert.Emails, ChatID: cfg.Alert.MAXChatID})
}

// connectMAX подключает адаптер MAX: поддельный в тестах и в разработке без
// токена, иначе настоящий через outbound и общий ограничитель (05 §2.8).
func (in *infra) connectMAX(cfg Config) error {
	if cfg.MAXFake() {
		fake := maxfake.New()
		fake.SetNow(in.clock.Now)
		in.max = fake
		return nil
	}
	log := in.log.With(logx.KeyModule, "max")
	httpClient, err := max.NewHTTPClient(cfg.MAX.CACertFile)
	if err != nil {
		return err
	}
	outMetrics, limitMetrics := in.integrationMetrics()
	out := outbound.New(in.pool, in.clock, log, outbound.Options{
		System: outbound.SystemMAX, HTTP: httpClient, Alerter: in.alerter, Metrics: outMetrics,
	})
	limiter := ratelimit.New(in.pool, in.clock, log, ratelimit.Options{Metrics: limitMetrics})
	in.max = max.New(out, limiter, max.Options{
		BaseURL: cfg.MAX.APIURL, Token: cfg.MAX.BotToken, GlobalRate: cfg.MAX.RateGlobal, ChatRate: cfg.MAX.RatePerChat,
		BotUserID: cfg.MAX.BotUserID,
	}, log)
	return nil
}

// platformJobs — служебные виды заданий этапа «Планировщик заданий и
// интеграционный слой»: разбор входящих событий, оповещения, зависшие объекты, очистка.
type platformJobs struct {
	dispatcher *inbound.Dispatcher
	alerts     *alert.Sender
	stuck      *stuck.Checker
	cleaner    *cleanup.Cleaner
}

func newPlatformJobs(cfg Config, in *infra, accounts *accountpg.Store) *platformJobs {
	log := in.log
	p := &platformJobs{
		dispatcher: inbound.NewDispatcher(in.pool, in.clock, log.With(logx.KeyModule, "inbound")),
		stuck:      stuck.New(in.alerter, in.clock, log.With(logx.KeyModule, "stuck")),
		cleaner:    cleanup.New(in.clock, log.With(logx.KeyModule, "cleanup")),
	}
	// В режиме api почты и MAX нет: интерфейсы остаются nil, задание send_alert там не регистрируется.
	p.alerts = in.alerter.NewSender(in.mail, in.max)

	p.stuck.Add(stuck.InboundEvents(in.pool, cfg.Scheduler.StuckInboundEventAge))
	p.stuck.Add(stuck.FailedJobs(in.pool))

	r := cfg.Retention
	for _, t := range []cleanup.Task{
		{Name: "outbound_request", Retention: r.OutboundRequest, Delete: outbound.DeleteRequests(in.pool)},
		{Name: "job_finished", Retention: r.JobFinished, Delete: job.DeleteFinished(in.pool, "succeeded", "cancelled")},
		{Name: "job_failed", Retention: r.JobFailed, Delete: job.DeleteFinished(in.pool, "failed")},
		{Name: "inbound_event", Retention: r.InboundEvent, Delete: inbound.DeleteEvents(in.pool)},
		{Name: "alert", Retention: r.Alert, Delete: alert.DeleteResolved(in.pool)},
		{Name: "worker_heartbeat", Retention: 24 * time.Hour, Delete: job.DeleteHeartbeats(in.pool)},
		{Name: "auth_code", Retention: r.AuthCode, Delete: accounts.DeleteExpiredAuthCodes},
		{Name: "session", Retention: r.Session, Delete: accounts.DeleteExpiredSessions},
	} {
		p.cleaner.Add(t)
	}
	return p
}

// register регистрирует служебные виды заданий и подключает оповещения к обработчику.
func (p *platformJobs) register(w *job.Worker, cfg Config, in *infra) {
	w.SetAlerter(in.alerter)
	w.SetDowntimeThreshold(cfg.Scheduler.DowntimeThreshold)
	p.dispatcher.Register(w)
	p.alerts.Register(w)
	p.stuck.Register(w)
	p.cleaner.Register(w)
}

// connectORD подключает адаптер Яндекс ОРД (05 §3): ordfake в тестах и в разработке без
// OAuth-токена (R-18), иначе настоящий через outbound и общий ограничитель (корзина ord:global, R-20).
func (in *infra) connectORD(ctx context.Context, cfg Config) {
	log := in.log.With(logx.KeyModule, "ord")
	if !cfg.ORD.Enabled {
		return
	}
	if cfg.ORDFake() {
		in.ord = ordfake.New()
		log.InfoContext(ctx, "ord adapter connected", logx.KeyOp, "Bootstrap.ConnectORD", "mode", "fake")
		return
	}
	outMetrics, limitMetrics := in.integrationMetrics()
	out := outbound.New(in.pool, in.clock, log, outbound.Options{
		System: outbound.SystemYandexORD, Alerter: in.alerter, Metrics: outMetrics,
	})
	limiter := ratelimit.New(in.pool, in.clock, log, ratelimit.Options{Metrics: limitMetrics})
	in.ord = ord.New(out, limiter, ord.Options{
		BaseURL: cfg.ORDBaseURL(), APIVersion: cfg.ORD.APIVersion, Token: cfg.ORD.OAuthToken, Rate: cfg.ORD.Rate,
	}, log)
	log.InfoContext(ctx, "ord adapter connected", logx.KeyOp, "Bootstrap.ConnectORD", "mode", "real",
		"base_url", cfg.ORDBaseURL(), "api_version", cfg.ORD.APIVersion)
}

// integrationMetrics — метрики outbound и ограничителя, созданные один раз на процесс.
func (in *infra) integrationMetrics() (*outbound.Metrics, *ratelimit.Metrics) {
	if in.reg != nil && in.outMetrics == nil {
		in.outMetrics, in.limitMetrics = outbound.NewMetrics(in.reg), ratelimit.NewMetrics(in.reg)
	}
	return in.outMetrics, in.limitMetrics
}

// connectRocketWork подключает адаптер Рокет Ворк по ROCKETWORK_MODE (R-1): rocketworkfake
// (в разработке — с общим файлом состояния для api и worker), заглушка disabled или
// HTTP-клиент чтения Public API v1 (live).
func (in *infra) connectRocketWork(ctx context.Context, cfg Config) error {
	const op = "Bootstrap.ConnectRocketWork"
	log := in.log.With(logx.KeyModule, "rocketwork")
	switch mode := cfg.RocketWorkMode(); mode {
	case RocketWorkFake:
		opts := []rocketworkfake.Option{rocketworkfake.WithLogger(log)}
		if cfg.RocketWork.FakeStatePath != "" {
			opts = append(opts, rocketworkfake.WithStatePath(cfg.RocketWork.FakeStatePath))
		}
		in.rocketwork = rocketworkfake.New(cfg.RocketWorkWebhookSecret(), opts...)
		log.InfoContext(ctx, "rocketwork adapter connected", logx.KeyOp, op, "mode", mode,
			"access_mode", cfg.RocketWorkAccess(), "purpose", cfg.RocketWork.Purpose,
			"state_path", cfg.RocketWork.FakeStatePath)
	case RocketWorkLive:
		outMetrics, limitMetrics := in.integrationMetrics()
		out := outbound.New(in.pool, in.clock, log, outbound.Options{
			System: outbound.SystemRocketWork, Alerter: in.alerter, Metrics: outMetrics, HTTP: rocketwork.NewHTTPClient(),
			// R-18: ПДн и ссылки онбординга и чека не сохраняются в outbound_request (запрос и ответ).
			Redact: rocketwork.RedactFields,
		})
		limiter := ratelimit.New(in.pool, in.clock, log, ratelimit.Options{Metrics: limitMetrics})
		var authorization rocketwork.WriteAuthorizer
		if cfg.rocketWorkAutomationAllowed() {
			if cfg.RocketWork.Target == "local-emulator" {
				authorization, _ = rocketwork.LocalEmulatorAuthorization(cfg.RocketWork.BaseURL)
			} else {
				authorization = rocketwork.AutomationAuthorization{}
			}
		}
		in.rocketwork = rocketwork.New(out, limiter, rocketwork.Options{BaseURL: cfg.RocketWork.BaseURL,
			Authorizer: authorization,
			Token:      cfg.RocketWork.Token, WebhookSecret: cfg.RocketWorkWebhookSecret(), PreviousWebhookSecret: []byte(cfg.RocketWork.PreviousWebhookSecret), Rate: cfg.RocketWork.Rate}, log)
		log.InfoContext(ctx, "rocketwork adapter connected", logx.KeyOp, op, "mode", mode,
			"contract", "Public API v1", "workflow_enabled", cfg.PayoutAppConfig().ProviderScope != nil, "live_verified", false)
	default:
		in.rocketwork = rocketwork.NewDisabled(log, in.clock.Now)
		log.InfoContext(ctx, "rocketwork adapter connected", logx.KeyOp, op, "mode", RocketWorkDisabled)
	}
	return nil
}
