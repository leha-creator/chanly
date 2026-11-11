package bootstrap

import (
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Окружения (07 §8).
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Режимы запуска исполняемого файла.
const (
	ModeAPI     = "api"
	ModeWorker  = "worker"
	ModeMigrate = "migrate"
	ModeLegal   = "legal"
	// ModeAdmin — роли администратора (maxim admin grant|revoke).
	ModeAdmin = "admin"
	// ModeDev — команды разработчика (maxim dev post-version): только development и test (R-23).
	ModeDev = "dev"
)

// maxShutdownDrain — верхний предел HTTP_SHUTDOWN_DRAIN: дольше stop_grace_period compose ждать нельзя.
const maxShutdownDrain = 60 * time.Second

// Config — вся конфигурация приложения. Источник — только переменные окружения (07 §8).
type Config struct {
	Env      string `env:"APP_ENV" envDefault:"development"`
	LogLevel string `env:"LOG_LEVEL"` // пусто: debug в разработке, info в остальных окружениях

	HTTPAddr      string `env:"HTTP_ADDR" envDefault:":8080"`
	MetricsAddr   string `env:"METRICS_ADDR" envDefault:":9090"`
	PublicBaseURL string `env:"PUBLIC_BASE_URL" envDefault:"http://localhost:8080"`
	// TrustedProxyCIDRs — адреса прокси, чьему X-Forwarded-For можно верить (edge-прокси стенда).
	TrustedProxyCIDRs []string `env:"TRUSTED_PROXY_CIDRS" envSeparator:","`
	CookieSecure      bool     `env:"COOKIE_SECURE" envDefault:"false"`
	// ShutdownDrain — пауза между SIGTERM и остановкой HTTP-сервера api: /readyz уже отвечает 503,
	// прокси убирает экземпляр до закрытия соединений (07 §8, выпуск без простоя).
	ShutdownDrain time.Duration `env:"HTTP_SHUTDOWN_DRAIN" envDefault:"0s"`

	DatabaseURL string `env:"DATABASE_URL"`
	// DBAppRole — роль, под которой работает приложение; миграции выдают ей права (04 §1).
	DBAppRole      string `env:"DB_APP_ROLE" envDefault:"maxim_app"`
	DBMaxConns     int32  `env:"DB_MAX_CONNS" envDefault:"10"`
	S3             S3Config
	SMTP           SMTPConfig
	MailFrom       string `env:"MAIL_FROM"`
	SecretsKey     string `env:"APP_SECRETS_KEY"`  // base64, 32 байта: шифрование секретов в job.payload (R-1)
	AuthCodePepper string `env:"AUTH_CODE_PEPPER"` // секрет HMAC кода входа (R-3)

	Auth         AuthConfig
	Channel      ChannelConfig
	Media        MediaConfig
	Placement    PlacementConfig
	Billing      BillingConfig
	TBank        TBankConfig
	Notification NotificationConfig

	ORD        ORDConfig
	RocketWork RocketWorkConfig
	MAX        MAXConfig
	Alert      AlertConfig
	Scheduler  SchedulerConfig
	Retention  RetentionConfig
}

// MAXConfig — MAX Bot API (05 §2.1, 05 §2.8).
type MAXConfig struct {
	APIURL string `env:"MAX_API_URL" envDefault:"https://platform-api2.max.ru"`
	// BotToken — токен бота; пусто в разработке — поддельный адаптер.
	BotToken string `env:"MAX_BOT_TOKEN"`
	// CACertFile — корневой сертификат Минцифры (PEM), добавляется к системным.
	CACertFile  string  `env:"MAX_CA_CERT_FILE"`
	RateGlobal  float64 `env:"MAX_RATE_GLOBAL" envDefault:"25"`
	RatePerChat float64 `env:"MAX_RATE_PER_CHAT" envDefault:"1"`
	// BotUserID — пользователь MAX бота платформы (R-8); 0 — узнать через GET /me.
	BotUserID int64 `env:"MAX_BOT_USER_ID"`
	// BotName — имя бота в инструкции подключения канала, например @maxim_ads_bot.
	BotName string `env:"MAX_BOT_NAME"`
	// WebhookPathSecret — секретная часть адреса /webhooks/max/{secret} (R-3, Q-35).
	WebhookPathSecret string `env:"MAX_WEBHOOK_PATH_SECRET"`
	// WebhookSecret — секрет подписки; MAX передаёт его в заголовке X-Max-Bot-Api-Secret.
	WebhookSecret string `env:"MAX_WEBHOOK_SECRET"`
	// WebhookURL — публичный адрес приёма без секретной части, например
	// https://api.example.ru/api/v1/webhooks/max. worker подписывает на него бота (R-5).
	WebhookURL string `env:"MAX_WEBHOOK_URL"`
}

// ChannelConfig — подключение каналов (S-12, A-41, D-11).
type ChannelConfig struct {
	LinkCodeTTL      time.Duration `env:"CHANNEL_LINK_CODE_TTL" envDefault:"60m"`
	LinkCodesPerHour int           `env:"CHANNEL_LINK_CODES_PER_HOUR" envDefault:"10"`
	UnclaimedTTL     time.Duration `env:"CHANNEL_UNCLAIMED_TTL" envDefault:"168h"`
	RknThreshold     int           `env:"RKN_SUBSCRIBERS_THRESHOLD" envDefault:"10000"`
	// RknRecheckInterval — через сколько подтверждённый номер РКН пора проверить снова (06 §6.3, R-17).
	RknRecheckInterval time.Duration `env:"RKN_RECHECK_INTERVAL" envDefault:"2160h"`
	// FeeRateBP — комиссия платформы сверху цены блогера, базисные пункты (06 §3, A-30).
	FeeRateBP int `env:"PLATFORM_FEE_RATE_BP" envDefault:"1500"`
	// Пороги флагов аномалий метрик (05 §2.7, R-15).
	MetricsSubscribersSpikeBP int `env:"METRICS_SUBSCRIBERS_SPIKE_BP" envDefault:"2000"`
	MetricsViewsMinBP         int `env:"METRICS_VIEWS_MIN_BP" envDefault:"100"`
	// Пределы интервалов недоступности (R-6).
	UnavailabilityMaxDays     int `env:"CHANNEL_UNAVAILABILITY_MAX_DAYS" envDefault:"90"`
	UnavailabilityHorizonDays int `env:"CHANNEL_UNAVAILABILITY_HORIZON_DAYS" envDefault:"365"`
	UnavailabilityMaxCount    int `env:"CHANNEL_UNAVAILABILITY_MAX_COUNT" envDefault:"50"`
}

// MediaConfig — медиатека и пост (этап «Медиатека и пост», 07 §4 «Файлы», A-23, A-38).
type MediaConfig struct {
	// MaxBytes — предел изображения; не больше предела MAX (50 МБ).
	MaxBytes int64 `env:"MEDIA_MAX_BYTES" envDefault:"52428800"`
	MinSide  int   `env:"MEDIA_MIN_SIDE" envDefault:"160"`
	// PreviewSide — длинная сторона превью медиатеки.
	PreviewSide    int `env:"MEDIA_PREVIEW_SIDE" envDefault:"640"`
	UploadsPerHour int `env:"MEDIA_UPLOADS_PER_HOUR" envDefault:"60"`
	// UploadConcurrency — одновременных загрузок на экземпляр api.
	UploadConcurrency int `env:"MEDIA_UPLOAD_CONCURRENCY" envDefault:"4"`
	// BuildConcurrency — одновременно строящихся изображений на экземпляр worker (R-20).
	BuildConcurrency int `env:"MEDIA_BUILD_CONCURRENCY" envDefault:"2"`
	// AlbumLimit — изображений в посте; не больше 10 до ответа на Q-05 (A-23).
	AlbumLimit int `env:"MEDIA_ALBUM_LIMIT" envDefault:"10"`
	// URLTTL — срок подписанных ссылок кабинета.
	URLTTL time.Duration `env:"MEDIA_URL_TTL" envDefault:"1h"`
	// RetentionDeleted — через сколько удалённый файл очищается из хранилища (R-19).
	RetentionDeleted time.Duration `env:"MEDIA_RETENTION_DELETED" envDefault:"720h"`
	// PostBodyLimit, PostFinalTextLimit — пределы текста поста (A-38).
	PostBodyLimit      int `env:"POST_BODY_LIMIT" envDefault:"3800"`
	PostFinalTextLimit int `env:"POST_FINAL_TEXT_LIMIT" envDefault:"4000"`
}

// PlacementConfig — заказ и размещения (03 §5, A-10, этап «Заказ и размещения»).
type PlacementConfig struct {
	// AcceptTimeout — T-01: срок принятия блогером.
	AcceptTimeout time.Duration `env:"PLACEMENT_ACCEPT_TIMEOUT" envDefault:"24h"`
	// ScheduleTimeout — T-13: срок планирования публикации (D-15).
	ScheduleTimeout time.Duration `env:"PLACEMENT_SCHEDULE_TIMEOUT" envDefault:"24h"`
	// WindowGuard — запас до конца окна дат: P-02, T-01, T-14, оплата (R-6, R-10, R-11).
	WindowGuard time.Duration `env:"PLACEMENT_WINDOW_GUARD" envDefault:"1h"`
	// MinLead — publish_at не раньше чем через MinLead; перенос — пока до publish_at больше (03 §5, P-07).
	MinLead time.Duration `env:"PLACEMENT_MIN_LEAD" envDefault:"1h"`
	// SlotStep — кратность publish_at (03 §5).
	SlotStep time.Duration `env:"PLACEMENT_SLOT_STEP" envDefault:"5m"`
	// ReeditGuard — правка поста отменяет размещения, у которых до конца окна меньше (R-15).
	ReeditGuard time.Duration `env:"PLACEMENT_REEDIT_GUARD" envDefault:"24h"`
	// CartMaxItems — позиций в корзине (R-8).
	CartMaxItems int `env:"CART_MAX_ITEMS" envDefault:"50"`
	// StuckScheduledAge — размещение в scheduled с прошедшим publish_at считается зависшим (07 §3).
	StuckScheduledAge time.Duration `env:"STUCK_SCHEDULED_PLACEMENT_AGE" envDefault:"35m"`
	// Control — публикация, снятие и контроль (этап «Автопостинг и контроль»).
	Control PlacementControlConfig
}

// PlacementControlConfig — сроки публикации, снятия, сверки и споров (03 §5, 05 §2.8, 07 §8).
type PlacementControlConfig struct {
	// PublishRetryWindow — T-03: повторы публикации от publish_at; дальше P-09 (R-4).
	PublishRetryWindow time.Duration `env:"PUBLISH_RETRY_WINDOW" envDefault:"30m"`
	// PublishRetryInterval — пауза между попытками публикации.
	PublishRetryInterval time.Duration `env:"PUBLISH_RETRY_INTERVAL" envDefault:"30s"`
	// PublishFindLookback — окно поиска своего поста после неизвестного исхода (R-5).
	PublishFindLookback time.Duration `env:"PUBLISH_FIND_LOOKBACK" envDefault:"40m"`
	// PublishMaxImages — изображений в посте (A-23, Q-05).
	PublishMaxImages int `env:"PUBLISH_MAX_IMAGES" envDefault:"10"`
	// RemoveRetryWindow — T-10: повторы снятия от remove_at (R-7).
	RemoveRetryWindow time.Duration `env:"REMOVE_RETRY_WINDOW" envDefault:"24h"`
	// ClaimWindow — T-05: окно претензий после снятия (R-14).
	ClaimWindow time.Duration `env:"PLACEMENT_CLAIM_WINDOW" envDefault:"24h"`
	// DisputeDeadline — T-06: срок решения по спору (R-11).
	DisputeDeadline time.Duration `env:"DISPUTE_DEADLINE" envDefault:"72h"`
	// TopViolationShare — предложение доли при top_violated, базисные пункты (03 §7).
	TopViolationShare int `env:"DISPUTE_TOP_VIOLATION_SHARE" envDefault:"5000"`
	// RatingPenaltyRefund, RatingPenaltyProportional — снижение рейтинга канала по решению спора
	// full_refund и proportional (A-08, 03 §6; R-13 этапа приёмки); 0 — без санкции.
	RatingPenaltyRefund       int `env:"DISPUTE_RATING_PENALTY_REFUND" envDefault:"10"`
	RatingPenaltyProportional int `env:"DISPUTE_RATING_PENALTY_PROPORTIONAL" envDefault:"5"`
	// ReconcileInterval — T-09: период сверки публикаций (A-27).
	ReconcileInterval time.Duration `env:"RECONCILE_INTERVAL" envDefault:"5m"`
	// ScheduledGrace — scheduled без публикации дольше этого после publish_at проверяется сверкой (05 §2.8).
	ScheduledGrace time.Duration `env:"RECONCILE_SCHEDULED_GRACE" envDefault:"5m"`
	// ViewsRefresh — просмотры публикации перечитываются не чаще (03 §8).
	ViewsRefresh time.Duration `env:"PUBLICATION_VIEWS_REFRESH" envDefault:"1h"`
	// StuckRemovalOverdueAge — публикация с removal_overdue дольше считается зависшей (07 §3).
	StuckRemovalOverdueAge time.Duration `env:"STUCK_REMOVAL_OVERDUE_AGE" envDefault:"1h"`
}

// Пределы MAX для изображений и текста (05 §2.6, A-38).
const (
	maxMediaBytes     = 50 << 20
	maxMediaSide      = 7680
	maxAlbumLimit     = 10
	maxFinalTextLimit = 4000
)

// AlertConfig — адресаты оповещений администратору (07 §3, R-5).
type AlertConfig struct {
	Emails    []string `env:"ALERT_EMAILS" envSeparator:","`
	MAXChatID int64    `env:"ALERT_MAX_CHAT_ID"`
}

// SchedulerConfig — пороги планировщика и контроля (07 §2, §3).
type SchedulerConfig struct {
	DowntimeThreshold    time.Duration `env:"DOWNTIME_THRESHOLD" envDefault:"30m"`
	StuckInboundEventAge time.Duration `env:"STUCK_INBOUND_EVENT_AGE" envDefault:"10m"`
}

// RetentionConfig — сроки хранения для задания cleanup (04 §8, R-10).
type RetentionConfig struct {
	OutboundRequest time.Duration `env:"RETENTION_OUTBOUND_REQUEST" envDefault:"4320h"` // 180 суток
	JobFinished     time.Duration `env:"RETENTION_JOB_FINISHED" envDefault:"720h"`      // 30 суток
	JobFailed       time.Duration `env:"RETENTION_JOB_FAILED" envDefault:"4320h"`
	InboundEvent    time.Duration `env:"RETENTION_INBOUND_EVENT" envDefault:"4320h"`
	AuthCode        time.Duration `env:"RETENTION_AUTH_CODE" envDefault:"168h"` // 7 суток после истечения
	Session         time.Duration `env:"RETENTION_SESSION" envDefault:"168h"`
	Alert           time.Duration `env:"RETENTION_ALERT" envDefault:"2160h"` // 90 суток после закрытия
	// ChannelUnavailability — интервалы недоступности после окончания (R-6: 30 суток).
	ChannelUnavailability time.Duration `env:"RETENTION_CHANNEL_UNAVAILABILITY" envDefault:"720h"`
}

// MAXFake сообщает, что вместо MAX работает поддельный адаптер: в тестовом
// окружении всегда, в разработке — без токена бота.
func (c Config) MAXFake() bool {
	return c.Env == EnvTest || (c.Env == EnvDevelopment && c.MAX.BotToken == "")
}

// S3Config — объектное хранилище (07 §1).
type S3Config struct {
	Endpoint      string `env:"S3_ENDPOINT"`
	Region        string `env:"S3_REGION" envDefault:"ru-central1"`
	BucketPrivate string `env:"S3_BUCKET_PRIVATE"`
	// BucketPublic — публичная корзина: анонимное чтение без перечисления (R-7).
	BucketPublic string `env:"S3_BUCKET_PUBLIC"`
	// PublicBaseURL — адрес чтения публичной корзины; ключ объекта дописывается в конец.
	PublicBaseURL string `env:"S3_PUBLIC_BASE_URL"`
	// PresignEndpoint — адрес хранилища в подписанных ссылках для браузера (в разработке
	// localhost:9000 вместо minio:9000); пусто — S3_ENDPOINT.
	PresignEndpoint string `env:"S3_PRESIGN_ENDPOINT"`
	AccessKey       string `env:"S3_ACCESS_KEY"`
	SecretKey       string `env:"S3_SECRET_KEY"`
	UseSSL          bool   `env:"S3_USE_SSL" envDefault:"true"`
}

// SMTPConfig — почтовый сервис (A-13, A-19).
type SMTPConfig struct {
	Host     string `env:"SMTP_HOST"`
	Port     int    `env:"SMTP_PORT" envDefault:"587"`
	User     string `env:"SMTP_USER"`
	Password string `env:"SMTP_PASSWORD"`
	// TLS: `starttls` (по умолчанию), `tls` — сразу TLS, `none` — без шифрования (только разработка).
	TLS string `env:"SMTP_TLS" envDefault:"starttls"`
}

// AuthConfig — параметры входа и сессий, значения по умолчанию из 07 §4.
type AuthConfig struct {
	LoginCodeTTL      time.Duration `env:"AUTH_LOGIN_CODE_TTL" envDefault:"600s"`
	LoginCodeAttempts int           `env:"AUTH_LOGIN_CODE_ATTEMPTS" envDefault:"5"`
	CodesPerEmailHour int           `env:"AUTH_CODES_PER_EMAIL_HOUR" envDefault:"5"`
	CodesPerIPHour    int           `env:"AUTH_CODES_PER_IP_HOUR" envDefault:"20"`
	SessionTTL        time.Duration `env:"AUTH_SESSION_TTL" envDefault:"720h"`
	AdminSessionTTL   time.Duration `env:"AUTH_ADMIN_SESSION_TTL" envDefault:"12h"`
}

// ConfigError перечисляет поля с ошибками. Значения полей в текст не попадают.
type ConfigError struct {
	Fields []string
}

func (e *ConfigError) Error() string {
	return "invalid configuration: " + strings.Join(e.Fields, ", ")
}

// LoadConfig читает конфигурацию из environ (обычно — env.ToMap(os.Environ()))
// и проверяет её для режима mode.
func LoadConfig(environ map[string]string, mode string) (Config, error) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environ}); err != nil {
		return Config{}, &ConfigError{Fields: parseErrorFields(err)}
	}
	if err := cfg.Validate(mode); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parseErrorFields(err error) []string {
	var agg env.AggregateError
	if !errors.As(err, &agg) {
		return []string{err.Error()}
	}
	keys := envKeys(reflect.TypeFor[Config]())
	fields := make([]string, 0, len(agg.Errors))
	for _, e := range agg.Errors {
		var pe env.ParseError
		if errors.As(e, &pe) {
			name := pe.Name
			if key, ok := keys[name]; ok {
				name = key
			}
			fields = append(fields, name+": invalid value")
			continue
		}
		fields = append(fields, "configuration: "+reflect.TypeOf(e).String())
	}
	return fields
}

// envKeys сопоставляет имя поля структуры с переменной окружения: ошибка
// разбора называет поле, а пользователю нужна переменная.
func envKeys(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Struct && f.Tag.Get("env") == "" && f.Type != reflect.TypeFor[time.Duration]() {
			maps.Copy(out, envKeys(f.Type))
			continue
		}
		if key, _, _ := strings.Cut(f.Tag.Get("env"), ","); key != "" {
			out[f.Name] = key
		}
	}
	return out
}

// Validate проверяет конфигурацию. Обязательность полей зависит от режима:
// migrate нужна только база, legal — база и хранилище.
func (c Config) Validate(mode string) error {
	var bad []string
	add := func(field, reason string) { bad = append(bad, field+": "+reason) }
	require := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			add(field, "required")
		}
	}

	switch c.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		add("APP_ENV", "must be development, test or production")
	}
	if _, ok := logx.ParseLevel(c.LogLevel, slog.LevelInfo); !ok {
		add("LOG_LEVEL", "must be debug, info, warn or error")
	}

	require("DATABASE_URL", c.DatabaseURL)
	if mode == ModeMigrate {
		require("DB_APP_ROLE", c.DBAppRole)
		return finish(bad)
	}
	if mode == ModeAdmin || mode == ModeDev {
		c.validateTBank(add)
		return finish(bad) // только база
	}
	if c.DBMaxConns < 1 {
		add("DB_MAX_CONNS", "must be positive")
	}

	// worker строит производные изображений и публикует их (R-6, R-7).
	needStorage := mode == ModeAPI || mode == ModeLegal || mode == ModeWorker
	needSecrets := mode == ModeAPI || mode == ModeWorker
	needMail := mode == ModeWorker
	fakes := c.Env == EnvTest // в тестовом окружении внешние системы поддельные

	if needStorage && !fakes {
		require("S3_ENDPOINT", c.S3.Endpoint)
		require("S3_BUCKET_PRIVATE", c.S3.BucketPrivate)
		require("S3_ACCESS_KEY", c.S3.AccessKey)
		require("S3_SECRET_KEY", c.S3.SecretKey)
		if mode != ModeLegal {
			require("S3_BUCKET_PUBLIC", c.S3.BucketPublic)
			if u, err := url.Parse(c.S3.PublicBaseURL); err != nil || u.Host == "" ||
				(u.Scheme != "https" && (c.Env == EnvProduction || u.Scheme != "http")) {
				add("S3_PUBLIC_BASE_URL", "must be an absolute URL (https in production)")
			}
		}
	}
	if needSecrets {
		if key, err := base64.StdEncoding.DecodeString(c.SecretsKey); err != nil || len(key) != 32 {
			add("APP_SECRETS_KEY", "must be 32 bytes in base64")
		}
	}
	if mode == ModeAPI {
		if len(c.AuthCodePepper) < 32 {
			add("AUTH_CODE_PEPPER", "must be at least 32 characters")
		}
		if c.Env == EnvProduction && !c.CookieSecure {
			add("COOKIE_SECURE", "must be true in production")
		}
		if c.ShutdownDrain < 0 || c.ShutdownDrain > maxShutdownDrain {
			add("HTTP_SHUTDOWN_DRAIN", "must be between 0s and 60s")
		}
		for _, cidr := range c.TrustedProxyCIDRs {
			if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
				add("TRUSTED_PROXY_CIDRS", "invalid CIDR")
				break
			}
		}
		a := c.Auth
		if a.LoginCodeTTL <= 0 || a.SessionTTL <= 0 || a.AdminSessionTTL <= 0 {
			add("AUTH_*_TTL", "must be positive")
		}
		if a.LoginCodeAttempts < 1 || a.CodesPerEmailHour < 1 || a.CodesPerIPHour < 1 {
			add("AUTH_* limits", "must be positive")
		}
		c.validateWebhookSecrets(add)
		ch := c.Channel
		if ch.LinkCodeTTL <= 0 || ch.LinkCodesPerHour < 1 || ch.UnclaimedTTL <= 0 || ch.RknThreshold < 1 {
			add("CHANNEL_*, RKN_SUBSCRIBERS_THRESHOLD", "must be positive")
		}
	}
	if mode == ModeAPI || mode == ModeWorker {
		c.validateCatalog(add)
		c.validateMedia(add)
		c.validatePlacement(add)
		c.validateORD(add)
		c.validateRocketWork(add)
		c.validateBilling(add)
		c.validateTBank(add)
		c.validateNotification(add)
	}
	if mode == ModeWorker {
		c.validateWorker(add)
	}
	if needMail && !fakes {
		require("SMTP_HOST", c.SMTP.Host)
		require("MAIL_FROM", c.MailFrom)
		switch c.SMTP.TLS {
		case "starttls", "tls":
		case "none":
			if c.Env == EnvProduction {
				add("SMTP_TLS", "none is not allowed in production")
			}
		default:
			add("SMTP_TLS", "must be starttls, tls or none")
		}
	}
	return finish(bad)
}

// validateWorker — MAX, оповещения, пороги и сроки хранения режима worker.
func (c Config) validateWorker(add func(field, reason string)) {
	if c.Env == EnvProduction {
		if strings.TrimSpace(c.MAX.BotToken) == "" {
			add("MAX_BOT_TOKEN", "required in production")
		}
		if len(c.Alert.Emails) == 0 && c.Alert.MAXChatID == 0 {
			add("ALERT_EMAILS", "at least one alert recipient (ALERT_EMAILS or ALERT_MAX_CHAT_ID) is required in production")
		}
		if u, err := url.Parse(c.MAX.WebhookURL); err != nil || u.Scheme != "https" || u.Host == "" {
			add("MAX_WEBHOOK_URL", "must be an https URL in production")
		}
		c.validateWebhookSecrets(add)
	}
	if u, err := url.Parse(c.MAX.APIURL); err != nil || u.Scheme == "" || u.Host == "" {
		add("MAX_API_URL", "must be an absolute URL")
	}
	if c.MAX.CACertFile != "" {
		if pem, err := os.ReadFile(c.MAX.CACertFile); err != nil {
			add("MAX_CA_CERT_FILE", "cannot be read")
		} else if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			add("MAX_CA_CERT_FILE", "no PEM certificates found")
		}
	}
	if c.MAX.RateGlobal <= 0 || c.MAX.RatePerChat <= 0 {
		add("MAX_RATE_*", "must be positive")
	}
	for _, e := range c.Alert.Emails {
		if _, err := mail.ParseAddress(strings.TrimSpace(e)); err != nil {
			add("ALERT_EMAILS", "invalid address")
			break
		}
	}
	if c.Scheduler.DowntimeThreshold <= 0 || c.Scheduler.StuckInboundEventAge <= 0 {
		add("DOWNTIME_THRESHOLD, STUCK_*", "must be positive")
	}
	r := c.Retention
	for _, d := range []time.Duration{r.OutboundRequest, r.JobFinished, r.JobFailed, r.InboundEvent, r.AuthCode, r.Session, r.Alert, r.ChannelUnavailability} {
		if d <= 0 {
			add("RETENTION_*", "must be positive")
			break
		}
	}
}

// validateMedia — пределы медиатеки и поста: не мягче пределов MAX (05 §2.6, A-23, A-38).
func (c Config) validateMedia(add func(field, reason string)) {
	m := c.Media
	if m.MaxBytes < 1 || m.MaxBytes > maxMediaBytes {
		add("MEDIA_MAX_BYTES", fmt.Sprintf("must be between 1 and %d", maxMediaBytes))
	}
	if m.MinSide < 1 || m.MinSide > maxMediaSide {
		add("MEDIA_MIN_SIDE", fmt.Sprintf("must be between 1 and %d", maxMediaSide))
	}
	if m.PreviewSide < 16 || m.PreviewSide > maxMediaSide {
		add("MEDIA_PREVIEW_SIDE", fmt.Sprintf("must be between 16 and %d", maxMediaSide))
	}
	if m.UploadsPerHour < 1 || m.UploadConcurrency < 1 || m.BuildConcurrency < 1 {
		add("MEDIA_UPLOADS_PER_HOUR, MEDIA_*_CONCURRENCY", "must be positive")
	}
	if m.AlbumLimit < 1 || m.AlbumLimit > maxAlbumLimit {
		add("MEDIA_ALBUM_LIMIT", fmt.Sprintf("must be between 1 and %d", maxAlbumLimit))
	}
	if m.URLTTL <= 0 || m.RetentionDeleted <= 0 {
		add("MEDIA_URL_TTL, MEDIA_RETENTION_DELETED", "must be positive")
	}
	if m.PostFinalTextLimit < 1 || m.PostFinalTextLimit > maxFinalTextLimit {
		add("POST_FINAL_TEXT_LIMIT", fmt.Sprintf("must be between 1 and %d", maxFinalTextLimit))
	}
	if m.PostBodyLimit < 1 || m.PostBodyLimit >= m.PostFinalTextLimit {
		add("POST_BODY_LIMIT", "must be positive and less than POST_FINAL_TEXT_LIMIT")
	}
}

// webhookSecretMin — наименьшая длина секретов Webhook.
const webhookSecretMin = 32

// validateWebhookSecrets — секреты приёма Webhook MAX (R-3): в продакшене обязательны,
// заданные — не короче webhookSecretMin.
func (c Config) validateWebhookSecrets(add func(field, reason string)) {
	for _, s := range []struct{ field, value string }{
		{"MAX_WEBHOOK_PATH_SECRET", c.MAX.WebhookPathSecret},
		{"MAX_WEBHOOK_SECRET", c.MAX.WebhookSecret},
	} {
		switch {
		case s.value == "" && c.Env == EnvProduction:
			add(s.field, "required in production")
		case s.value != "" && len(s.value) < webhookSecretMin:
			add(s.field, fmt.Sprintf("must be at least %d characters", webhookSecretMin))
		}
	}
}

func finish(bad []string) error {
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return &ConfigError{Fields: bad}
}

// Level — уровень журнала с учётом окружения (07 §5.1).
func (c Config) Level() slog.Level {
	def := slog.LevelInfo
	if c.Env == EnvDevelopment {
		def = slog.LevelDebug
	}
	l, _ := logx.ParseLevel(c.LogLevel, def)
	return l
}

// SecretsKeyBytes — расшифрованный APP_SECRETS_KEY. Вызывать после Validate.
func (c Config) SecretsKeyBytes() []byte {
	key, _ := base64.StdEncoding.DecodeString(c.SecretsKey)
	return key
}

// LogValue выводит конфигурацию без секретов: она пишется в журнал на DEBUG.
func (c Config) LogValue() slog.Value {
	secret := func(v string) string {
		if v == "" {
			return ""
		}
		return "***"
	}
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("log_level", c.Level().String()),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("metrics_addr", c.MetricsAddr),
		slog.String("public_base_url", c.PublicBaseURL),
		slog.Any("trusted_proxy_cidrs", c.TrustedProxyCIDRs),
		slog.Bool("cookie_secure", c.CookieSecure),
		slog.Duration("http_shutdown_drain", c.ShutdownDrain),
		slog.String("database_url", redactURL(c.DatabaseURL)),
		slog.String("db_app_role", c.DBAppRole),
		slog.Int("db_max_conns", int(c.DBMaxConns)),
		slog.String("s3_endpoint", c.S3.Endpoint),
		slog.String("s3_bucket_private", c.S3.BucketPrivate),
		slog.String("s3_bucket_public", c.S3.BucketPublic),
		slog.String("s3_public_base_url", c.S3.PublicBaseURL),
		slog.String("s3_presign_endpoint", c.S3.PresignEndpoint),
		slog.String("s3_access_key", secret(c.S3.AccessKey)),
		slog.String("s3_secret_key", secret(c.S3.SecretKey)),
		slog.Bool("s3_use_ssl", c.S3.UseSSL),
		slog.String("smtp_host", c.SMTP.Host),
		slog.Int("smtp_port", c.SMTP.Port),
		slog.String("smtp_user", secret(c.SMTP.User)),
		slog.String("smtp_password", secret(c.SMTP.Password)),
		slog.String("smtp_tls", c.SMTP.TLS),
		slog.String("app_secrets_key", secret(c.SecretsKey)),
		slog.String("auth_code_pepper", secret(c.AuthCodePepper)),
		slog.String("auth_login_code_ttl", c.Auth.LoginCodeTTL.String()),
		slog.Int("auth_login_code_attempts", c.Auth.LoginCodeAttempts),
		slog.Int("auth_codes_per_email_hour", c.Auth.CodesPerEmailHour),
		slog.Int("auth_codes_per_ip_hour", c.Auth.CodesPerIPHour),
		slog.String("auth_session_ttl", c.Auth.SessionTTL.String()),
		slog.String("auth_admin_session_ttl", c.Auth.AdminSessionTTL.String()),
		slog.String("max_api_url", c.MAX.APIURL),
		slog.String("max_bot_token", secret(c.MAX.BotToken)),
		slog.String("max_ca_cert_file", c.MAX.CACertFile),
		slog.Float64("max_rate_global", c.MAX.RateGlobal),
		slog.Float64("max_rate_per_chat", c.MAX.RatePerChat),
		slog.String("max_bot_name", c.MAX.BotName),
		slog.String("max_webhook_url", c.MAX.WebhookURL),
		slog.String("max_webhook_path_secret", secret(c.MAX.WebhookPathSecret)),
		slog.String("max_webhook_secret", secret(c.MAX.WebhookSecret)),
		slog.String("channel_link_code_ttl", c.Channel.LinkCodeTTL.String()),
		slog.Int("channel_link_codes_per_hour", c.Channel.LinkCodesPerHour),
		slog.String("channel_unclaimed_ttl", c.Channel.UnclaimedTTL.String()),
		slog.Int("rkn_subscribers_threshold", c.Channel.RknThreshold),
		slog.String("rkn_recheck_interval", c.Channel.RknRecheckInterval.String()),
		slog.Int("platform_fee_rate_bp", c.Channel.FeeRateBP),
		slog.Int("metrics_subscribers_spike_bp", c.Channel.MetricsSubscribersSpikeBP),
		slog.Int("metrics_views_min_bp", c.Channel.MetricsViewsMinBP),
		slog.Int("channel_unavailability_max_days", c.Channel.UnavailabilityMaxDays),
		slog.Int("channel_unavailability_horizon_days", c.Channel.UnavailabilityHorizonDays),
		slog.Int("channel_unavailability_max_count", c.Channel.UnavailabilityMaxCount),
		slog.Int64("media_max_bytes", c.Media.MaxBytes),
		slog.Int("media_min_side", c.Media.MinSide),
		slog.Int("media_preview_side", c.Media.PreviewSide),
		slog.Int("media_uploads_per_hour", c.Media.UploadsPerHour),
		slog.Int("media_upload_concurrency", c.Media.UploadConcurrency),
		slog.Int("media_build_concurrency", c.Media.BuildConcurrency),
		slog.Int("media_album_limit", c.Media.AlbumLimit),
		slog.String("media_url_ttl", c.Media.URLTTL.String()),
		slog.String("media_retention_deleted", c.Media.RetentionDeleted.String()),
		slog.Int("post_body_limit", c.Media.PostBodyLimit),
		slog.Int("post_final_text_limit", c.Media.PostFinalTextLimit),
		slog.String("placement_accept_timeout", c.Placement.AcceptTimeout.String()),
		slog.String("placement_schedule_timeout", c.Placement.ScheduleTimeout.String()),
		slog.String("placement_window_guard", c.Placement.WindowGuard.String()),
		slog.String("placement_min_lead", c.Placement.MinLead.String()),
		slog.String("placement_slot_step", c.Placement.SlotStep.String()),
		slog.String("placement_reedit_guard", c.Placement.ReeditGuard.String()),
		slog.Int("cart_max_items", c.Placement.CartMaxItems),
		slog.String("stuck_scheduled_placement_age", c.Placement.StuckScheduledAge.String()),
		slog.Int("alert_emails", len(c.Alert.Emails)),
		slog.Bool("alert_max_chat", c.Alert.MAXChatID != 0),
		slog.String("downtime_threshold", c.Scheduler.DowntimeThreshold.String()),
		slog.String("stuck_inbound_event_age", c.Scheduler.StuckInboundEventAge.String()),
		slog.Bool("ord_enabled", c.ORD.Enabled),
		slog.Bool("ord_fake", c.ORDFake()),
		slog.String("ord_api_url", c.ORDBaseURL()),
		slog.String("ord_api_version", c.ORD.APIVersion),
		slog.String("ord_oauth_token", secret(c.ORD.OAuthToken)),
		slog.Float64("ord_rate", c.ORD.Rate),
		slog.String("ord_kktu_code", c.ORD.KKTUCode),
		slog.String("ord_platform_inn", logx.MaskINN(c.ORD.PlatformINN)),
		slog.String("ord_platform_legal_type", c.ORD.PlatformLegalType),
		slog.String("ord_platform_type", c.ORD.PlatformType),
		slog.String("ord_status_timeout", c.ORD.StatusTimeout.String()),
		slog.String("ord_erid_wait", c.ORD.EridWait.String()),
		slog.String("ord_invoice_type_initial", c.ORD.InvoiceTypeInitial),
		slog.String("ord_invoice_type_publisher", c.ORD.InvoiceTypePublisher),
		slog.String("ord_invoice_number_prefix", c.ORD.InvoiceNumberPrefix),
		slog.Bool("ord_report_auto_send", c.ORD.ReportAutoSend),
		slog.Int("ord_report_error_day", c.ORD.ReportErrorDay),
		slog.String("rocketwork_mode", c.RocketWorkMode()),
		slog.String("rocketwork_base_url", c.RocketWork.BaseURL),
		slog.String("rocketwork_token", secret(c.RocketWork.Token)),
		slog.String("rocketwork_webhook_secret", secret(c.RocketWork.WebhookSecret)),
		slog.Float64("rocketwork_rate", c.RocketWork.Rate),
		slog.String("rocketwork_deposit_check_interval", c.RocketWork.DepositCheckInterval.String()),
		slog.Int64("rocketwork_deposit_tolerance", c.RocketWork.DepositTolerance),
		slog.String("payout_poll_fast", c.RocketWork.PollFast.String()),
		slog.String("payout_poll_slow", c.RocketWork.PollSlow.String()),
		slog.Int64("payout_npd_annual_limit", c.RocketWork.NPDAnnualLimit),
		slog.String("platform_bank_account", logx.MaskAccount(c.BillingSupplier().BankAccount)),
		slog.String("invoice_ttl", c.Billing.InvoiceTTL.String()),
		slog.Int("invoice_rate_per_user", c.Billing.InvoiceRatePerUser),
		slog.Int("invoice_rate_per_ip", c.Billing.InvoiceRatePerIP),
		slog.String("ledger_check_interval", c.Billing.LedgerCheckInterval.String()),
		slog.Int64("ledger_manual_second_approval_threshold", c.Billing.ManualSecondApproval),
		slog.String("admin_approval_ttl", c.Billing.ApprovalTTL.String()),
		slog.String("closing_acts_at", fmt.Sprintf("%d %s", c.Billing.ClosingActsDay, c.Billing.ClosingActsTime)),
		slog.String("cabinet_base_url", c.CabinetBaseURL()),
		slog.Int("notification_send_attempts", c.Notification.SendAttempts),
		slog.String("notification_retention", c.Notification.Retention.String()),
	)
}

// redactURL убирает пароль из строки подключения вида postgres://user:pass@host/db.
func redactURL(u string) string {
	scheme := strings.Index(u, "://")
	at := strings.LastIndexByte(u, '@')
	if scheme < 0 || at < scheme {
		return u
	}
	creds := u[scheme+3 : at]
	if colon := strings.IndexByte(creds, ':'); colon >= 0 {
		creds = creds[:colon] + ":***"
	}
	return fmt.Sprintf("%s%s%s", u[:scheme+3], creds, u[at:])
}

// validateCatalog — параметры каталога (этап «Каталог»): комиссия, пороги метрик,
// пределы интервалов недоступности, повторная проверка РКН.
func (c Config) validateCatalog(add func(field, reason string)) {
	ch := c.Channel
	if ch.FeeRateBP < 0 || ch.FeeRateBP > 10000 {
		add("PLATFORM_FEE_RATE_BP", "must be 0..10000")
	}
	if ch.MetricsSubscribersSpikeBP < 1 {
		add("METRICS_SUBSCRIBERS_SPIKE_BP", "must be positive")
	}
	if ch.MetricsViewsMinBP < 0 || ch.MetricsViewsMinBP > 10000 {
		add("METRICS_VIEWS_MIN_BP", "must be 0..10000")
	}
	if ch.UnavailabilityMaxDays < 1 || ch.UnavailabilityHorizonDays < 1 || ch.UnavailabilityMaxCount < 1 {
		add("CHANNEL_UNAVAILABILITY_*", "must be positive")
	}
	if ch.RknRecheckInterval <= 0 {
		add("RKN_RECHECK_INTERVAL", "must be positive")
	}
	if ch.RknThreshold < 1 {
		add("RKN_SUBSCRIBERS_THRESHOLD", "must be positive")
	}
}

// maxCartItems — верхняя граница CART_MAX_ITEMS: оформление блокирует каналы корзины в одной транзакции.
const maxCartItems = 200

// validatePlacement — сроки и правила времени размещений (03 §5, R-10, R-13, R-15).
func (c Config) validatePlacement(add func(field, reason string)) {
	p := c.Placement
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"PLACEMENT_ACCEPT_TIMEOUT", p.AcceptTimeout}, {"PLACEMENT_SCHEDULE_TIMEOUT", p.ScheduleTimeout},
		{"PLACEMENT_WINDOW_GUARD", p.WindowGuard}, {"PLACEMENT_MIN_LEAD", p.MinLead},
		{"PLACEMENT_REEDIT_GUARD", p.ReeditGuard}, {"STUCK_SCHEDULED_PLACEMENT_AGE", p.StuckScheduledAge},
	} {
		if d.value <= 0 {
			add(d.field, "must be positive")
		}
	}
	// Слоты одинаковы в каждом часе: шаг — целое число минут, делящее час.
	if p.SlotStep < time.Minute || p.SlotStep%time.Minute != 0 || time.Hour%p.SlotStep != 0 {
		add("PLACEMENT_SLOT_STEP", "must be whole minutes dividing an hour")
	}
	if p.WindowGuard > p.AcceptTimeout {
		add("PLACEMENT_WINDOW_GUARD", "must not exceed PLACEMENT_ACCEPT_TIMEOUT")
	}
	if p.CartMaxItems < 1 || p.CartMaxItems > maxCartItems {
		add("CART_MAX_ITEMS", fmt.Sprintf("must be between 1 and %d", maxCartItems))
	}
	c.validateControl(add)
}

// validateControl — сроки публикации, снятия и контроля (R-4, R-5, R-7, R-11, R-12).
func (c Config) validateControl(add func(field, reason string)) {
	k := c.Placement.Control
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"PUBLISH_RETRY_WINDOW", k.PublishRetryWindow}, {"PUBLISH_RETRY_INTERVAL", k.PublishRetryInterval},
		{"PUBLISH_FIND_LOOKBACK", k.PublishFindLookback}, {"REMOVE_RETRY_WINDOW", k.RemoveRetryWindow},
		{"PLACEMENT_CLAIM_WINDOW", k.ClaimWindow}, {"DISPUTE_DEADLINE", k.DisputeDeadline},
		{"RECONCILE_INTERVAL", k.ReconcileInterval}, {"RECONCILE_SCHEDULED_GRACE", k.ScheduledGrace},
		{"PUBLICATION_VIEWS_REFRESH", k.ViewsRefresh}, {"STUCK_REMOVAL_OVERDUE_AGE", k.StuckRemovalOverdueAge},
	} {
		if d.value <= 0 {
			add(d.field, "must be positive")
		}
	}
	if k.PublishRetryInterval >= k.PublishRetryWindow {
		add("PUBLISH_RETRY_INTERVAL", "must be less than PUBLISH_RETRY_WINDOW")
	}
	if k.PublishFindLookback <= k.PublishRetryWindow {
		add("PUBLISH_FIND_LOOKBACK", "must exceed PUBLISH_RETRY_WINDOW")
	}
	if k.PublishMaxImages < 1 || k.PublishMaxImages > maxAlbumLimit {
		add("PUBLISH_MAX_IMAGES", fmt.Sprintf("must be between 1 and %d", maxAlbumLimit))
	}
	if k.TopViolationShare < 0 || k.TopViolationShare > 10000 {
		add("DISPUTE_TOP_VIOLATION_SHARE", "must be between 0 and 10000")
	}
	for _, v := range []struct {
		name string
		val  int
	}{{"DISPUTE_RATING_PENALTY_REFUND", k.RatingPenaltyRefund}, {"DISPUTE_RATING_PENALTY_PROPORTIONAL", k.RatingPenaltyProportional}} {
		if v.val < 0 || v.val > 100 {
			add(v.name, "must be between 0 and 100")
		}
	}
}
