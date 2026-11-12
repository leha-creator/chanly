package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	accountapp "github.com/leha-creator/chanly/internal/account/app"
	accounthttp "github.com/leha-creator/chanly/internal/account/httpapi"
	accountjobs "github.com/leha-creator/chanly/internal/account/jobs"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	adminapp "github.com/leha-creator/chanly/internal/admin/app"
	adminhttp "github.com/leha-creator/chanly/internal/admin/httpapi"
	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	billinghttp "github.com/leha-creator/chanly/internal/billing/httpapi"
	billingjobs "github.com/leha-creator/chanly/internal/billing/jobs"
	billingpg "github.com/leha-creator/chanly/internal/billing/postgres"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	channelhttp "github.com/leha-creator/chanly/internal/channel/httpapi"
	channeljobs "github.com/leha-creator/chanly/internal/channel/jobs"
	"github.com/leha-creator/chanly/internal/integration/mail"
	"github.com/leha-creator/chanly/internal/integration/mail/mailfake"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerhttp "github.com/leha-creator/chanly/internal/ledger/httpapi"
	ledgerjobs "github.com/leha-creator/chanly/internal/ledger/jobs"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	markingapp "github.com/leha-creator/chanly/internal/marking/app"
	markinghttp "github.com/leha-creator/chanly/internal/marking/httpapi"
	markingjobs "github.com/leha-creator/chanly/internal/marking/jobs"
	markingpg "github.com/leha-creator/chanly/internal/marking/postgres"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	notificationhttp "github.com/leha-creator/chanly/internal/notification/httpapi"
	notificationjobs "github.com/leha-creator/chanly/internal/notification/jobs"
	notificationpg "github.com/leha-creator/chanly/internal/notification/postgres"
	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	payouthttp "github.com/leha-creator/chanly/internal/payout/httpapi"
	payoutjobs "github.com/leha-creator/chanly/internal/payout/jobs"
	payoutpg "github.com/leha-creator/chanly/internal/payout/postgres"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementhttp "github.com/leha-creator/chanly/internal/placement/httpapi"
	placementjobs "github.com/leha-creator/chanly/internal/placement/jobs"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
	"github.com/leha-creator/chanly/internal/platform/adminref"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/cleanup"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/metrics"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/realtime"
	"github.com/leha-creator/chanly/internal/platform/secretbox"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/platform/storage/storagefake"
	"github.com/leha-creator/chanly/internal/platform/tx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	posthttp "github.com/leha-creator/chanly/internal/post/httpapi"
	postjobs "github.com/leha-creator/chanly/internal/post/jobs"
)

// infra — общие ресурсы процесса: база, часы, журнал и адаптеры внешних систем.
// В APP_ENV=test адаптеры поддельные; в разработке Mailpit и MinIO — настоящие SMTP и S3.
type infra struct {
	pool    *pgxpool.Pool
	clock   clock.Clock
	log     *slog.Logger
	storage storage.ObjectStorage
	mail    mailer
	max     maxAPI
	// ord — Яндекс ОРД (integration/ord, ordfake); только worker, nil при ORD_ENABLED=false.
	ord     bridge.OrdClient
	alerter *alert.Alerter
	// reg — реестр метрик процесса; nil в служебных командах.
	reg *metrics.Registry
	box *secretbox.Box
	// rocketwork — Рокет Ворк (rocketwork.Client, rocketwork.Disabled, rocketworkfake); nil — disabled.
	rocketwork bridge.RocketWorkAPI
	// outMetrics, limitMetrics — общие метрики outbound и ограничителя: регистрируются один раз
	// на процесс, сколько бы адаптеров ни подключилось.
	outMetrics   *outbound.Metrics
	limitMetrics *ratelimit.Metrics
	// life — признак остановки процесса для /readyz (выпуск без простоя).
	life lifecycle
}

// newInfra подключает адаптеры, нужные режиму.
// commandClockKey — часы команд maxim admin и maxim dev, запущенных в тестах рядом с собранным
// приложением (clock.Manual): задания и переходы получают то же время, что api и worker.
type commandClockKey struct{}

func withCommandClock(ctx context.Context, clk clock.Clock) context.Context {
	return context.WithValue(ctx, commandClockKey{}, clk)
}

func newInfra(ctx context.Context, cfg Config, mode string, pool *pgxpool.Pool, log *slog.Logger, reg *metrics.Registry) (*infra, error) {
	in := &infra{pool: pool, clock: clock.System{}, log: log, reg: reg}
	if clk, ok := ctx.Value(commandClockKey{}).(clock.Clock); ok {
		in.clock = clk
	}
	in.alerter = newAlerter(cfg, in)
	fakes := cfg.Env == EnvTest
	// worker строит и публикует производные изображений (R-6, R-7): ему тоже нужно хранилище.
	if mode == ModeAPI || mode == ModeLegal || mode == ModeWorker || mode == modeAdminMoney || mode == modeDevSeed {
		if fakes {
			in.storage = storagefake.New()
		} else {
			opts := storage.S3Options{
				Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.BucketPrivate,
				AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, UseSSL: cfg.S3.UseSSL,
				PresignEndpoint: cfg.S3.PresignEndpoint,
			}
			if mode != ModeLegal {
				opts.PublicBucket, opts.PublicBaseURL = cfg.S3.BucketPublic, cfg.S3.PublicBaseURL
			}
			s, err := storage.NewS3(ctx, opts, in.clock, log.With(logx.KeyModule, "storage"))
			if err != nil {
				return nil, err
			}
			in.storage = s
		}
	}
	if mode == ModeWorker {
		if fakes {
			in.mail = mailfake.New()
		} else {
			m, err := mail.New(mail.Options{
				Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, User: cfg.SMTP.User, Password: cfg.SMTP.Password,
				TLS: cfg.SMTP.TLS, From: cfg.MailFrom,
			}, in.clock, log.With(logx.KeyModule, "mail"))
			if err != nil {
				return nil, err
			}
			in.mail = m
		}
		if err := in.connectMAX(cfg); err != nil {
			return nil, err
		}
		in.connectORD(ctx, cfg)
	}
	// Рокет Ворк: подпись вебхука (api), задания (worker), сверка депозита и команды (admin, dev).
	if mode == ModeAPI || mode == ModeWorker || mode == modeAdminMoney || mode == ModeAdmin || mode == ModeDev || mode == modeDevSeed {
		if err := in.connectRocketWork(ctx, cfg); err != nil {
			return nil, err
		}
	}
	if mode == ModeAPI || mode == ModeWorker {
		box, err := secretbox.New(cfg.SecretsKeyBytes())
		if err != nil {
			return nil, err
		}
		in.box = box
	}
	return in, nil
}

// modules — собранные модули приложения.
type modules struct {
	cfg         Config
	in          *infra
	account     *accountapp.Service
	accountHTTP *accounthttp.Handlers
	channel     *channelapp.Service
	channelHTTP *channelhttp.Handlers
	post        *postapp.Service
	postHTTP    *posthttp.Handlers
	// placement — заказы и размещения; placementStore — для метрики и команды разработчика.
	placement      *placementapp.Service
	placementHTTP  *placementhttp.Handlers
	placementStore *placementpg.Store
	// ledger — журнал проводок и удержания.
	ledger *ledgerapp.Service
	// billing — счета, выписки и закрывающие документы.
	billing      *billingapp.Service
	billingHTTP  *billinghttp.Handlers
	billingStore *billingpg.Store
	ledgerHTTP   *ledgerhttp.Handlers
	ledgerStore  *ledgerpg.Store
	// marking — объекты ОРД и erid; markingStore — для метрики.
	marking      *markingapp.Service
	markingStore *markingpg.Store
	// markingHTTP — ошибки и объекты ОРД для администратора (этап «API администратора»).
	markingHTTP *markinghttp.Handlers
	// payout — выплаты блогерам через Рокет Ворк; payoutStore — для команд администратора.
	payout      *payoutapp.Service
	payoutStore *payoutpg.Store
	payoutHTTP  *payouthttp.Handlers
	// notification — уведомления; notificationStore — для метрики.
	notification      *notificationapp.Service
	notificationHTTP  *notificationhttp.Handlers
	notificationStore *notificationpg.Store
	// admin — второе подтверждение, журнал действий, счётчики меню (этап «API администратора»).
	admin     *adminapp.Service
	adminHTTP *adminhttp.Handlers
	platform  *platformJobs
	// txRunner и queue нужны приёму Webhook (api).
	txRunner tx.Runner
	queue    *job.Queue
}

// newModules связывает порты модулей с реализациями.
func newModules(cfg Config, in *infra) *modules {
	log := in.log
	txRunner := tx.NewRunner(in.pool, log.With(logx.KeyModule, "db"))
	queue := job.NewQueue(in.pool, in.clock, log.With(logx.KeyModule, "job"))
	auditLog := audit.New(in.pool, in.clock, log.With(logx.KeyModule, "audit"))
	accountStore := accountpg.New(in.pool, log.With(logx.KeyModule, "account"))

	deps := accountapp.Deps{
		Settings: accountStore.AccountSettings(),
		Users:    accountStore.Users(), AuthCodes: accountStore.AuthCodes(), Sessions: accountStore.Sessions(),
		Organizations: accountStore.Organizations(), Admin: accountStore.Organizations(), PersonalData: accountStore.PersonalData(), Platform: accountStore.Organizations(), Legal: accountStore.LegalDocuments(),
		Sender: in.mail, Uploads: auditLog, Tx: txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: log,
		Config: accountapp.Config{
			LoginCodeTTL: cfg.Auth.LoginCodeTTL, LoginCodeAttempts: cfg.Auth.LoginCodeAttempts,
			CodesPerEmailHour: cfg.Auth.CodesPerEmailHour, CodesPerIPHour: cfg.Auth.CodesPerIPHour,
			SessionTTL: cfg.Auth.SessionTTL, AdminSessionTTL: cfg.Auth.AdminSessionTTL,
			Pepper: []byte(cfg.AuthCodePepper),
		},
	}
	if in.storage != nil {
		deps.Storage = in.storage
	}
	if in.box != nil {
		deps.Sealer = in.box
	}
	svc := accountapp.New(deps)
	channelSvc, channelStore := newChannelService(cfg, in, txRunner, queue, auditLog)
	notificationSvc, notificationStore := newNotificationService(cfg, in, txRunner, queue, accountStore, channelStore)
	channelSvc.Notifier = bridge.ChannelNotifier{Svc: notificationSvc}
	postSvc := newPostService(cfg, in, txRunner, queue, auditLog, accountStore)
	ledgerSvc, ledgerStore := newLedgerService(cfg, in, txRunner, auditLog)
	billingSvc, billingStore := newBillingService(cfg, in, txRunner, auditLog, accountStore, ledgerSvc, notificationSvc)
	placementSvc, placementStore := newPlacementService(cfg, in, txRunner, queue, auditLog, channelSvc, postSvc, ledgerSvc, notificationSvc)
	payoutSvc, payoutStore := newPayoutService(cfg, in, txRunner, queue, auditLog, accountStore, ledgerSvc, placementSvc, placementStore, notificationSvc)
	// Описания операций баланса — номера заказов и счетов (R-9).
	ledgerSvc.References = bridge.LedgerReferences{Placements: placementStore.Placements(), Invoices: billingStore}
	billingSvc.Placements = bridge.BillingPlacements{Placements: placementStore.Placements()}
	markingSvc, markingStore := newMarkingService(cfg, in, txRunner, queue, auditLog, accountStore, postSvc, channelSvc,
		placementStore.OrdReport())
	platform := newPlatformJobs(cfg, in, accountStore)
	platform.stuck.Add(stuckScheduledCheck(cfg, placementSvc))
	platform.stuck.Add(stuckRemovalCheck(cfg, placementSvc))
	for _, c := range payoutSvc.StuckChecks() {
		platform.stuck.Add(c)
	}
	platform.cleaner.Add(cleanup.Task{
		Name: "channel_unavailability", Retention: cfg.Retention.ChannelUnavailability, Delete: channelStore.DeleteEndedUnavailability,
	})
	platform.cleaner.Add(cleanup.Task{Name: "notification", Retention: cfg.Notification.Retention, Delete: notificationStore.DeleteBefore})
	platform.cleaner.Add(cleanup.Task{Name: "email_change", Retention: cfg.Retention.AuthCode, Delete: accountStore.DeleteExpiredEmailChanges})
	m := &modules{
		cfg:               cfg,
		in:                in,
		account:           svc,
		accountHTTP:       accounthttp.New(svc, accounthttp.Options{CookieSecure: cfg.CookieSecure}, log),
		channel:           channelSvc,
		channelHTTP:       channelhttp.New(channelSvc, log),
		post:              postSvc,
		postHTTP:          newPostHandlers(cfg, postSvc, in),
		placement:         placementSvc,
		placementHTTP:     placementhttp.New(placementSvc, log),
		placementStore:    placementStore,
		ledger:            ledgerSvc,
		billing:           billingSvc,
		billingHTTP:       billinghttp.New(billingSvc, log),
		billingStore:      billingStore,
		ledgerHTTP:        ledgerhttp.New(ledgerSvc, log),
		ledgerStore:       ledgerStore,
		marking:           markingSvc,
		markingStore:      markingStore,
		markingHTTP:       markinghttp.New(markingSvc, adminref.New(in.pool), log),
		notification:      notificationSvc,
		notificationHTTP:  notificationhttp.New(notificationSvc, log),
		notificationStore: notificationStore,
		payout:            payoutSvc,
		payoutStore:       payoutStore,
		payoutHTTP:        payouthttp.New(payoutSvc, log),
		platform:          platform,
		txRunner:          txRunner,
		queue:             queue,
	}
	m.wireAdmin()
	if cfg.ORD.Enabled {
		m.wireMarking()
	}
	return m
}

// registerJobs регистрирует обработчики заданий всех модулей.
func (m *modules) registerJobs(w *job.Worker) {
	m.platform.register(w, m.cfg, m.in)
	accountjobs.Register(w, m.account)
	channeljobs.Register(w, m.platform.dispatcher, m.channel)
	postjobs.Register(w, m.post)
	placementjobs.Register(w, m.placement, placementjobs.Options{
		PublishRetryWindow: m.cfg.Placement.Control.PublishRetryWindow,
		ReconcileInterval:  m.cfg.Placement.Control.ReconcileInterval,
	})
	placementjobs.RegisterEvents(m.platform.dispatcher, m.placement)
	closingHour, closingMinute := m.cfg.ClosingActsAt()
	billingjobs.RegisterBank(w, m.billing, m.cfg.TBank.PollInterval)
	billingjobs.Register(w, m.billing, billingjobs.Options{ExpireInterval: m.cfg.Billing.InvoiceExpireInterval,
		ClosingDay: m.cfg.Billing.ClosingActsDay, ClosingHour: closingHour, ClosingMinute: closingMinute})
	ledgerjobs.Register(w, m.ledger, m.cfg.Billing.LedgerCheckInterval)
	payoutjobs.Register(w, m.platform.dispatcher, m.payout, payoutjobs.Options{DepositCheckInterval: m.cfg.RocketWork.DepositCheckInterval})
	notificationjobs.Register(w, m.notification)
	if m.cfg.ORD.Enabled {
		markingjobs.Register(w, m.marking)
		markingjobs.RegisterReports(w, m.marking)
		markingjobs.RegisterReports(w, m.marking)
	}
}

// apiImpl — реализация всех операций: обработчики модулей поверх заглушек 501.
type apiImpl struct {
	fallback
	*rwCheckProxy
	*accounthttp.Handlers
	*channelHandlers
	*postHandlers
	*placementHandlers
	*billingHandlers
	*ledgerHandlers
	*payoutHandlers
	*notificationHandlers
	*adminHandlers
	*markingHandlers
}

// apiHandler собирает HTTP-обработчик режима api.
func apiHandler(ctx context.Context, cfg Config, in *infra, m *modules, reg *metrics.Registry) (http.Handler, error) {
	trusted, err := httpx.ParsePrefixes(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: %w", err)
	}
	log := in.log.With(logx.KeyModule, "http")
	hub := realtime.NewHub(128)
	go realtime.NewReader(in.pool, hub, in.log.With(logx.KeyModule, "realtime")).Run(ctx)
	return newHandler(serverDeps{
		Log:     in.log,
		Clock:   in.clock,
		Metrics: reg,
		Impl: apiImpl{rwCheckProxy: &rwCheckProxy{cfg: cfg, log: log, audit: m.admin.Audit}, Handlers: m.accountHTTP, channelHandlers: m.channelHTTP, postHandlers: m.postHTTP,
			placementHandlers: m.placementHTTP, billingHandlers: m.billingHTTP, ledgerHandlers: m.ledgerHTTP,
			payoutHandlers: m.payoutHTTP, notificationHandlers: m.notificationHTTP, adminHandlers: m.adminHTTP,
			markingHandlers: m.markingHTTP},
		// Webhook MAX — сырое тело и заголовок секрета, вне strict server (R-4).
		Routes: map[string]http.Handler{
			maxWebhookPattern:        newMAXWebhook(cfg, in, m.txRunner, m.queue, httpx.ClientInfo(trusted)),
			rocketworkWebhookPattern: newRocketWorkWebhook(in, m.payout, m.txRunner, m.queue, httpx.ClientInfo(trusted)),
		},
		Streams: map[string]http.Handler{
			"GET " + BaseURL + "/events":       realtime.Handler(hub, m.accountHTTP.AuthenticateStream, false, log),
			"GET " + BaseURL + "/admin/events": realtime.Handler(hub, m.accountHTTP.AuthenticateStream, true, log),
		},
		Ready:         in.life.ready(readiness(in.pool)),
		Authenticated: httpx.Authenticated,
		Before: []httpx.Middleware{
			httpx.ClientInfo(trusted),
			httpx.Session(log, m.accountHTTP.Authenticate),
			httpx.CSRF(log, csrfExempt),
		},
		ServeDocs: cfg.Env != EnvProduction,
	})
}

// csrfExempt — вход и Webhook внешних систем не несут токен CSRF.
func csrfExempt(r *http.Request) bool {
	p := r.URL.Path
	return p == BaseURL+"/auth/code" || p == BaseURL+"/auth/login" || strings.HasPrefix(p, BaseURL+"/webhooks/")
}

func readiness(pool *pgxpool.Pool) func(ctx context.Context) error {
	ping := db.Ping(pool)
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return ping(ctx)
	}
}
