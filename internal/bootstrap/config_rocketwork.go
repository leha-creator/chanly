package bootstrap

import (
	"math"
	"net/url"
	"strings"
	"time"

	payoutapp "github.com/leha-creator/chanly/internal/payout/app"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/money"
	checkdomain "github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

// Режимы адаптера Рокет Ворк (R-1 этапа выплат).
const (
	// RocketWorkFake — rocketworkfake: тесты и разработка.
	RocketWorkFake = "fake"
	// RocketWorkDisabled — сервис не подключён: онбординг недоступен, задания откладываются.
	RocketWorkDisabled = "disabled"
	// RocketWorkLive — Public API v1; workflow requires explicit configuration.
	RocketWorkLive = "live"
)

// RocketWorkConfig — Рокет Ворк и выплаты блогерам (05 §4, 06 §2.3, этап «Выплаты блогерам»).
type RocketWorkConfig struct {
	CheckGatewayURL      string `env:"ROCKETWORK_CHECK_GATEWAY_URL"`
	CheckGatewayToken    string `env:"ROCKETWORK_CHECK_GATEWAY_TOKEN"`
	ControlDatabaseURL   string `env:"ROCKETWORK_CONTROL_DATABASE_URL"`
	PolicyDatabaseURL    string `env:"ROCKETWORK_POLICY_DATABASE_URL"`
	ArmingID             string `env:"ROCKETWORK_CHECK_ARMING_ID"`
	AccessMode           string `env:"ROCKETWORK_ACCESS_MODE"`
	Purpose              string `env:"ROCKETWORK_PURPOSE"`
	ProductionApproved   bool   `env:"ROCKETWORK_PRODUCTION_APPROVED"`
	WithdrawalCompanyINN string `env:"ROCKETWORK_WITHDRAWAL_COMPANY_INN"`
	WithdrawalBIC        string `env:"ROCKETWORK_WITHDRAWAL_BIC"`
	WithdrawalAccount    string `env:"ROCKETWORK_WITHDRAWAL_ACCOUNT"`
	WithdrawalExecutorID int64  `env:"ROCKETWORK_WITHDRAWAL_EXECUTOR_ID"`
	TaskTitle            string `env:"ROCKETWORK_TASK_TITLE"`
	TaskDescription      string `env:"ROCKETWORK_TASK_DESCRIPTION"`
	WorkflowEnabled      bool   `env:"ROCKETWORK_WORKFLOW_ENABLED"`
	Target               string `env:"ROCKETWORK_TARGET"`
	AccountKey           string `env:"ROCKETWORK_ACCOUNT_KEY"`
	ContractMode         string `env:"ROCKETWORK_CONTRACT_MODE"`
	DocumentName         string `env:"ROCKETWORK_DOCUMENT_NAME"`
	DocumentNumber       string `env:"ROCKETWORK_DOCUMENT_NUMBER"`
	DocumentDate         string `env:"ROCKETWORK_DOCUMENT_DATE"`
	PeriodFrom           string `env:"ROCKETWORK_PERIOD_FROM"`
	PeriodTo             string `env:"ROCKETWORK_PERIOD_TO"`
	ActRequired          *bool  `env:"ROCKETWORK_ACT_REQUIRED"`
	TaxRetention         *bool  `env:"ROCKETWORK_TAX_RETENTION"`
	MaxCommission        *int64 `env:"ROCKETWORK_MAX_COMMISSION_KOPECKS"`
	// Mode — fake | disabled | live; пусто — fake вне production и disabled в production.
	Mode string `env:"ROCKETWORK_MODE"`
	// BaseURL — адрес API; пусто — https://api.rocketwork.ru из OpenAPI.
	BaseURL string `env:"ROCKETWORK_BASE_URL"`
	// Token — токен кабинета заказчика (секрет); обязателен при live.
	Token string `env:"ROCKETWORK_TOKEN"`
	// Rate — запросов в секунду на все экземпляры (корзина rocketwork:global).
	Rate float64 `env:"ROCKETWORK_RATE" envDefault:"5"`
	// WebhookSecret — Bearer-токен подписки в live; с fake — HMAC-ключ разработки.
	WebhookSecret         string `env:"ROCKETWORK_WEBHOOK_SECRET"`
	PreviousWebhookSecret string `env:"ROCKETWORK_PREVIOUS_WEBHOOK_SECRET"`
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

// RocketWorkAccess defaults live transports to read-only, including old configs.
func (c Config) RocketWorkAccess() checkdomain.Access {
	if c.RocketWork.AccessMode == "" {
		return checkdomain.ReadOnly
	}
	return checkdomain.Access(c.RocketWork.AccessMode)
}

func (c Config) rocketWorkAutomationAllowed() bool {
	r := c.RocketWork
	if c.RocketWorkAccess() != checkdomain.Automation || r.Purpose != checkdomain.Business {
		return false
	}
	return (c.Env == EnvProduction && r.Target == "production" && r.ProductionApproved) ||
		(c.Env != EnvProduction && r.Target == "local-emulator" && rwOrigin(r.Target, r.BaseURL))
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
	result := payoutapp.Config{
		ProviderEnabled: c.RocketWorkMode() != RocketWorkDisabled,
		PollFast:        r.PollFast, PollSlow: r.PollSlow, ContractorPollMaxAge: r.ContractorPollMaxAge,
		DepositTolerance: money.Kopecks(r.DepositTolerance), NPDAnnualLimit: money.Kopecks(r.NPDAnnualLimit),
		SecondApproval: money.Kopecks(c.Billing.ManualSecondApproval),
	}
	origin := r.BaseURL
	if origin == "" {
		origin = "https://api.rocketwork.ru"
	}
	if c.RocketWorkMode() == RocketWorkLive && r.WorkflowEnabled && c.rocketWorkAutomationAllowed() && rwOrigin(r.Target, origin) && r.AccountKey != "" {
		result.ProviderScope = &payoutdomain.ProviderScope{Environment: r.Target, Account: r.AccountKey}
		if r.WithdrawalCompanyINN != "" && r.WithdrawalBIC != "" && r.WithdrawalAccount != "" && r.WithdrawalExecutorID > 0 {
			result.WithdrawalDestination = &payoutdomain.WithdrawalDestination{CompanyINN: r.WithdrawalCompanyINN, BIC: r.WithdrawalBIC, AccountNumber: r.WithdrawalAccount, ExecutorID: r.WithdrawalExecutorID}
		}
	}
	if r.ActRequired != nil && r.TaxRetention != nil && r.MaxCommission != nil {
		result.AgreementTemplate = &payoutdomain.AgreementTerms{Currency: "RUB", Title: r.TaskTitle, Description: r.TaskDescription, SignatureForm: r.ContractMode, DocumentName: r.DocumentName, DocumentNumber: r.DocumentNumber, DocumentDate: r.DocumentDate, PeriodFrom: r.PeriodFrom, PeriodTo: r.PeriodTo, ActRequired: *r.ActRequired, TaxRetention: *r.TaxRetention, MaxCommission: money.Kopecks(*r.MaxCommission)}
	}
	return result
}

// validateRocketWorkIsolation also applies to admin/dev commands, which otherwise
// validate only their database settings.
func (c Config) validateRocketWorkIsolation(add func(field, reason string)) {
	r := c.RocketWork
	mode := c.RocketWorkMode()
	if r.ControlDatabaseURL != "" || r.PolicyDatabaseURL != "" || r.ArmingID != "" {
		add("ROCKETWORK_CONTROL_DATABASE_URL", "control credentials and arming configuration belong only to isolated rocketwork-check")
	}
	if c.Env != EnvProduction && r.Target != "local-emulator" && r.Token != "" {
		add("ROCKETWORK_TOKEN", "ordinary dev/test processes must not receive the primary account token")
	}
	if mode != RocketWorkLive && r.Token != "" {
		add("ROCKETWORK_TOKEN", "fake/disabled must not receive provider credentials")
	}
	access := c.RocketWorkAccess()
	switch access {
	case checkdomain.ReadOnly:
	case checkdomain.Controlled:
		// The ordinary API/worker configuration never carries runner authority.
		add("ROCKETWORK_ACCESS_MODE", "controlled requires the isolated diagnostic runner")
	case checkdomain.Automation:
		if !c.rocketWorkAutomationAllowed() {
			add("ROCKETWORK_ACCESS_MODE", "automation requires production business admission or a local emulator")
		}
	default:
		add("ROCKETWORK_ACCESS_MODE", "must be readonly, controlled or automation")
	}
	if r.Purpose != "" && r.Purpose != checkdomain.Business && r.Purpose != checkdomain.Diagnostic {
		add("ROCKETWORK_PURPOSE", "must be business or diagnostic")
	}
}

// validateRocketWork — параметры выплат режимов api и worker (R-1).
func (c Config) validateRocketWork(add func(field, reason string)) {
	r := c.RocketWork
	mode := c.RocketWorkMode()
	c.validateRocketWorkIsolation(add)
	if mode == RocketWorkLive {
		origin := r.BaseURL
		if origin == "" {
			origin = "https://api.rocketwork.ru"
		}
		if !rwOrigin(r.Target, origin) {
			add("ROCKETWORK_TARGET", "explicit target must match provider origin")
		}
	}
	if r.WorkflowEnabled {
		if !c.rocketWorkAutomationAllowed() {
			add("ROCKETWORK_WORKFLOW_ENABLED", "requires explicit automation capability")
		}
		if mode != RocketWorkLive {
			add("ROCKETWORK_WORKFLOW_ENABLED", "requires live adapter")
		}
		origin := r.BaseURL
		if origin == "" {
			origin = "https://api.rocketwork.ru"
		}
		if !rwOrigin(r.Target, origin) {
			add("ROCKETWORK_TARGET", "target must match provider origin")
		}
		if r.Target == "production" && !r.ProductionApproved {
			add("ROCKETWORK_PRODUCTION_APPROVED", "workflow requires separate production admission after live acceptance")
		}
		if strings.TrimSpace(r.AccountKey) == "" {
			add("ROCKETWORK_ACCOUNT_KEY", "required for workflow identity")
		}
		if r.ActRequired == nil || r.TaxRetention == nil || r.MaxCommission == nil {
			add("ROCKETWORK_CONTRACT_MODE", "explicit act, tax and commission policy required")
		}
		if template := c.PayoutAppConfig().AgreementTemplate; template != nil {
			template.Amount, template.ExecutorID, template.EmploymentKind = 1, 1, "selfemployed"
			if template.Validate() != nil {
				add("ROCKETWORK_CONTRACT_MODE", "complete, valid agreement template required")
			}
		}
	}
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
			local := r.Target == "local-emulator" && rwOrigin(r.Target, r.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && !local) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				add("ROCKETWORK_BASE_URL", "must be an HTTPS origin without credentials, path, query or fragment")
			}
		}
		if r.Rate <= 0 || math.IsNaN(r.Rate) || math.IsInf(r.Rate, 0) {
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
