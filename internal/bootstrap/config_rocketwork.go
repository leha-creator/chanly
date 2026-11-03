package bootstrap

import (
	"net/url"
	"strings"
	"time"

	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Режимы адаптера Рокет Ворк (R-1 этапа выплат).
const (
	// RocketWorkFake — rocketworkfake: тесты и разработка.
	RocketWorkFake = "fake"
	// RocketWorkDisabled — сервис не подключён: онбординг недоступен, задания откладываются.
	RocketWorkDisabled = "disabled"
	// RocketWorkLive — HTTP-клиент по предполагаемому контракту (Q-10 … Q-12).
	RocketWorkLive = "live"
)

// RocketWorkConfig — Рокет Ворк и выплаты блогерам (05 §4, 06 §2.3, этап «Выплаты блогерам»).
type RocketWorkConfig struct {
	// Mode — fake | disabled | live; пусто — fake вне production и disabled в production.
	Mode string `env:"ROCKETWORK_MODE"`
	// BaseURL — адрес API; пусто — rocketwork.DefaultBaseURL (ПРЕДПОЛОЖЕНИЕ (Q-10)).
	BaseURL string `env:"ROCKETWORK_BASE_URL"`
	// Token — токен кабинета заказчика (секрет); обязателен при live.
	Token string `env:"ROCKETWORK_TOKEN"`
	// Rate — запросов в секунду на все экземпляры (корзина rocketwork:global).
	Rate float64 `env:"ROCKETWORK_RATE" envDefault:"5"`
	// WebhookSecret — ключ подписи вебхуков (секрет); обязателен при live, с fake — ключ разработки.
	WebhookSecret string `env:"ROCKETWORK_WEBHOOK_SECRET"`
	// FakeStatePath — общий файл состояния rocketworkfake для api и worker (разработка).
	FakeStatePath string `env:"ROCKETWORK_FAKE_STATE_PATH"`
	// DepositCheckInterval — check_rocketwork_deposit (R-12).
	DepositCheckInterval time.Duration `env:"ROCKETWORK_DEPOSIT_CHECK_INTERVAL" envDefault:"1h"`
	// DepositTolerance — допуск сверки депозита с журналом, копейки (06 §2.4).
	DepositTolerance int64 `env:"ROCKETWORK_DEPOSIT_TOLERANCE" envDefault:"100"`
	// ContractorPollMaxAge — не опрашивать invited/pending дольше.
	ContractorPollMaxAge time.Duration `env:"ROCKETWORK_CONTRACTOR_POLL_MAX_AGE" envDefault:"720h"`
	// PollFast, PollSlow — опрос выплаты в первый час и дальше (R-10).
	PollFast time.Duration `env:"PAYOUT_POLL_FAST" envDefault:"5m"`
	PollSlow time.Duration `env:"PAYOUT_POLL_SLOW" envDefault:"1h"`
	// NPDAnnualLimit — годовой лимит дохода самозанятого, копейки (06 §4).
	NPDAnnualLimit int64 `env:"PAYOUT_NPD_ANNUAL_LIMIT" envDefault:"240000000"`
}

// RocketWorkMode — режим с подстановкой значения по умолчанию.
func (c Config) RocketWorkMode() string {
	if m := strings.TrimSpace(c.RocketWork.Mode); m != "" {
		return m
	}
	if c.Env == EnvProduction {
		return RocketWorkDisabled
	}
	return RocketWorkFake
}

// rocketWorkDevSecret — ключ подписи rocketworkfake в разработке и тестах без ROCKETWORK_WEBHOOK_SECRET.
const rocketWorkDevSecret = "rocketwork-dev-webhook-secret-0000000"

// RocketWorkWebhookSecret — ключ подписи; с подделкой без значения — ключ разработки.
func (c Config) RocketWorkWebhookSecret() []byte {
	if c.RocketWork.WebhookSecret == "" && c.RocketWorkMode() == RocketWorkFake && c.Env != EnvProduction {
		return []byte(rocketWorkDevSecret)
	}
	return []byte(c.RocketWork.WebhookSecret)
}

// PayoutAppConfig — параметры модуля выплат.
func (c Config) PayoutAppConfig() payoutapp.Config {
	r := c.RocketWork
	return payoutapp.Config{
		ProviderEnabled: c.RocketWorkMode() != RocketWorkDisabled,
		PollFast:        r.PollFast, PollSlow: r.PollSlow, ContractorPollMaxAge: r.ContractorPollMaxAge,
		DepositTolerance: money.Kopecks(r.DepositTolerance), NPDAnnualLimit: money.Kopecks(r.NPDAnnualLimit),
		SecondApproval: money.Kopecks(c.Billing.ManualSecondApproval),
	}
}

// validateRocketWork — параметры выплат режимов api и worker (R-1).
func (c Config) validateRocketWork(add func(field, reason string)) {
	r := c.RocketWork
	mode := c.RocketWorkMode()
	switch mode {
	case RocketWorkFake, RocketWorkDisabled, RocketWorkLive:
	default:
		add("ROCKETWORK_MODE", "must be fake, disabled or live")
		return
	}
	if mode == RocketWorkFake && c.Env == EnvProduction {
		add("ROCKETWORK_MODE", "fake is not allowed in production")
	}
	if mode == RocketWorkLive {
		if strings.TrimSpace(r.Token) == "" {
			add("ROCKETWORK_TOKEN", "required in live mode")
		}
		if r.BaseURL != "" {
			u, err := url.Parse(r.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && c.Env == EnvProduction) || u.Scheme == "" {
				add("ROCKETWORK_BASE_URL", "must be an absolute (https in production) URL")
			}
		}
		if r.Rate <= 0 {
			add("ROCKETWORK_RATE", "must be positive")
		}
	}
	if mode == RocketWorkLive && len(r.WebhookSecret) < 16 {
		add("ROCKETWORK_WEBHOOK_SECRET", "required in live mode (at least 16 characters)")
	}
	if r.WebhookSecret != "" && len(r.WebhookSecret) < 16 {
		add("ROCKETWORK_WEBHOOK_SECRET", "must be at least 16 characters")
	}
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"ROCKETWORK_DEPOSIT_CHECK_INTERVAL", r.DepositCheckInterval},
		{"ROCKETWORK_CONTRACTOR_POLL_MAX_AGE", r.ContractorPollMaxAge},
		{"PAYOUT_POLL_FAST", r.PollFast}, {"PAYOUT_POLL_SLOW", r.PollSlow},
	} {
		if d.value <= 0 {
			add(d.field, "must be positive")
		}
	}
	if r.PollFast > r.PollSlow {
		add("PAYOUT_POLL_FAST", "must not exceed PAYOUT_POLL_SLOW")
	}
	if r.DepositTolerance < 0 {
		add("ROCKETWORK_DEPOSIT_TOLERANCE", "must not be negative")
	}
	if r.NPDAnnualLimit <= 0 {
		add("PAYOUT_NPD_ANNUAL_LIMIT", "must be positive")
	}
}
