package bootstrap

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/tbank"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

type TBankConfig struct {
	AccessMode         string        `env:"TBANK_ACCESS_MODE" envDefault:"readonly"`
	DevRealEnabled     bool          `env:"TBANK_DEV_REAL_ENABLED"`
	ProductionApproved bool          `env:"TBANK_PRODUCTION_APPROVED"`
	DevPaymentCode     string        `env:"TBANK_DEV_PAYMENT_CODE"`
	Mode               string        `env:"TBANK_MODE" envDefault:"disabled"`
	BaseURL            string        `env:"TBANK_API_URL" envDefault:"https://business.tbank.ru/openapi/sandbox"`
	Token              string        `env:"TBANK_TOKEN"`
	CACertFile         string        `env:"TBANK_CA_CERT_FILE"`
	Start              string        `env:"TBANK_IMPORT_FROM"`
	PollInterval       time.Duration `env:"TBANK_POLL_INTERVAL" envDefault:"5m"`
	Period             time.Duration `env:"TBANK_PERIOD" envDefault:"24h"`
	Overlap            time.Duration `env:"TBANK_OVERLAP" envDefault:"168h"`
	StaleAfter         time.Duration `env:"TBANK_STALE_AFTER" envDefault:"30m"`
	Rate               float64       `env:"TBANK_RATE" envDefault:"5"`
}

func (c Config) validateTBank(add func(string, string)) {
	b := c.TBank
	// Disabled remains usable with stale configuration and carries no bank capability.
	if b.Mode == "" || b.Mode == "disabled" {
		return
	}
	access := c.TBankAccess()
	switch access {
	case domain.BankReadOnly, domain.BankControlled, domain.BankAutomation:
	default:
		add("TBANK_ACCESS_MODE", "must be readonly, controlled or automation")
	}
	if access == domain.BankControlled {
		if strings.TrimSpace(c.ORD.PlatformINN) == "" {
			add("ORD_PLATFORM_INN", "explicit platform INN required for controlled own-transfer filtering")
		}
		if strings.TrimSpace(c.ORD.PlatformName) == "" {
			add("ORD_PLATFORM_NAME", "explicit invoice supplier required for real dev payments")
		}
		if c.Env != EnvDevelopment || !b.DevRealEnabled || b.Mode != "live" || !checkEndpoint(b.BaseURL, tbank.ProductionURL) {
			add("TBANK_ACCESS_MODE", "controlled requires development, production bank endpoint and TBANK_DEV_REAL_ENABLED")
		}
		if !domain.ValidDevPaymentCode(b.DevPaymentCode) {
			add("TBANK_DEV_PAYMENT_CODE", "required: 3-48 ASCII letters, digits, underscores or hyphens")
		}
	}
	if access == domain.BankAutomation && b.Mode == "live" {
		allowed := (c.Env == EnvProduction && b.ProductionApproved && checkEndpoint(b.BaseURL, tbank.ProductionURL)) || (c.Env == EnvDevelopment && checkEndpoint(b.BaseURL, tbank.SandboxURL))
		if !allowed {
			add("TBANK_ACCESS_MODE", "real unrestricted automation requires production approval; dev uses controlled")
		}
	}
	switch b.Mode {
	case "", "disabled":
		return
	case "fake":
		if c.Env == EnvProduction {
			add("TBANK_MODE", "fake forbidden in production")
		}
	case "live":
		if c.Env == EnvTest {
			add("TBANK_MODE", "test requires fake or disabled")
		}
		if c.Env == EnvDevelopment && checkEndpoint(b.BaseURL, tbank.ProductionURL) && !b.DevRealEnabled {
			add("TBANK_DEV_REAL_ENABLED", "explicit admission required for real bank access in development")
		}
		if strings.TrimSpace(b.Token) == "" {
			add("TBANK_TOKEN", "required")
		}
		if !checkEndpoint(b.BaseURL, tbank.SandboxURL) && !checkEndpoint(b.BaseURL, tbank.ProductionURL) {
			add("TBANK_API_URL", "unapproved endpoint")
		}
		if c.Env == EnvProduction && !checkEndpoint(b.BaseURL, tbank.ProductionURL) {
			add("TBANK_API_URL", "production endpoint required")
		}
		if !accountRe.MatchString(c.Billing.PlatformBankAccount) {
			add("PLATFORM_BANK_ACCOUNT", "explicit account required")
		}
	default:
		add("TBANK_MODE", "expected disabled, fake or live")
	}
	start, err := time.Parse(time.RFC3339, b.Start)
	if err != nil || start.Before(time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)) {
		add("TBANK_IMPORT_FROM", "UTC start since June 2023 required")
	}
	if b.PollInterval < time.Minute || b.PollInterval > time.Hour {
		add("TBANK_POLL_INTERVAL", "expected 1m to 1h")
	}
	if b.Period < time.Hour || b.Period > 31*24*time.Hour {
		add("TBANK_PERIOD", "expected 1h to 744h")
	}
	if b.Overlap < 24*time.Hour || b.Overlap > 31*24*time.Hour {
		add("TBANK_OVERLAP", "expected 24h to 744h")
	}
	if b.StaleAfter < b.PollInterval {
		add("TBANK_STALE_AFTER", "must cover poll interval")
	}
	if math.IsNaN(b.Rate) || math.IsInf(b.Rate, 0) || b.Rate <= 0 || b.Rate > 20 {
		add("TBANK_RATE", "expected positive rate up to 20")
	}
	if _, err := max.NewHTTPClient(b.CACertFile); err != nil {
		add("TBANK_CA_CERT_FILE", "cannot load trust roots")
	}
}
func (c Config) bankSyncConfig() billingapp.BankConfig {
	start, _ := time.Parse(time.RFC3339, c.TBank.Start)
	access := c.TBankAccess()
	if c.TBank.Mode == "fake" {
		access = domain.BankAutomation
	}
	if c.TBank.Mode == "" || c.TBank.Mode == "disabled" {
		access = domain.BankReadOnly
	}
	return billingapp.BankConfig{Access: access, DevRealEnabled: c.TBank.DevRealEnabled, DevPaymentCode: c.TBank.DevPaymentCode, Start: start.UTC(), Period: c.TBank.Period, Overlap: c.TBank.Overlap, StaleAfter: c.TBank.StaleAfter}
}

func (c Config) TBankAccess() string {
	if c.TBank.AccessMode == "" {
		return domain.BankReadOnly
	}
	return c.TBank.AccessMode
}
func (c Config) bankAutoSyncEnabled() bool {
	if c.TBank.Mode == "fake" {
		return c.Env != EnvProduction
	}
	if c.TBank.Mode != "live" {
		return false
	}
	switch c.TBankAccess() {
	case domain.BankControlled:
		enabled := c.bankSyncConfig().DevPaymentsEnabled()
		return c.Env == EnvDevelopment && c.TBank.DevRealEnabled && c.ORD.PlatformINN != "" && c.ORD.PlatformName != "" && checkEndpoint(c.TBank.BaseURL, tbank.ProductionURL) && enabled
	case domain.BankAutomation:
		return (c.Env == EnvProduction && c.TBank.ProductionApproved && checkEndpoint(c.TBank.BaseURL, tbank.ProductionURL)) || (c.Env == EnvDevelopment && checkEndpoint(c.TBank.BaseURL, tbank.SandboxURL))
	default:
		return false
	}
}

// The fake is deliberately empty. Domain/e2e tests inject their own operations;
// fake mode cannot assert live bank access or fabricate advertiser deposits.
type emptyBank struct{}

func (emptyBank) ReadPage(context.Context, domain.BankQuery) (domain.BankPage, error) {
	return domain.BankPage{}, nil
}
func (in *infra) bankSource(c Config, diagnostic bool) (domain.BankSource, error) {
	switch c.TBank.Mode {
	case "fake":
		return emptyBank{}, nil
	case "live":
		httpClient, err := max.NewHTTPClient(c.TBank.CACertFile)
		if err != nil {
			return nil, err
		}
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		om, lm := in.integrationMetrics()
		outOpts := outbound.Options{System: "tbank", HTTP: httpClient, NoRetry: diagnostic, Metrics: om}
		// A typed nil *alert.Alerter becomes a non-nil interface. Diagnostics
		// have no sender, so leave the option unset and use outbound's no-op.
		if in.alerter != nil {
			outOpts.Alerter = in.alerter
		}
		out := outbound.New(in.pool, in.clock, in.log, outOpts)
		limit := ratelimit.New(in.pool, in.clock, in.log, ratelimit.Options{Metrics: lm})
		return tbank.New(out, limit, tbank.Options{BaseURL: c.TBank.BaseURL, Token: c.TBank.Token, Account: c.Billing.PlatformBankAccount, INN: c.BillingAppConfig().Supplier.INN, Rate: c.TBank.Rate}, in.log), nil
	default:
		return nil, errors.New("bank source disabled")
	}
}
