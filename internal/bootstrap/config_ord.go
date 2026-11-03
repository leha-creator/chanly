package bootstrap

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/integration/ord"
	markingdomain "github.com/leha-creator/chanly/internal/marking/domain"
)

// ORDConfig — Яндекс ОРД (05 §3, этап «Маркировка через Яндекс ОРД»).
type ORDConfig struct {
	// Enabled — маркировка подключена (R-18). В production выключить нельзя.
	Enabled bool `env:"ORD_ENABLED" envDefault:"true"`
	// APIURL — адрес ОРД; пусто в development и test — тестовый контур (05 §3.8).
	APIURL string `env:"ORD_API_URL"`
	// APIVersion — версия API в адресе (05 §3.8).
	APIVersion string `env:"ORD_API_VERSION" envDefault:"v8"`
	// OAuthToken — токен пользователя кабинета ОРД (секрет); пусто в development и test — ordfake.
	OAuthToken string `env:"ORD_OAUTH_TOKEN"`
	// Rate — запросов в секунду на все экземпляры (Q-15, R-20).
	Rate float64 `env:"ORD_RATE" envDefault:"5"`
	// KKTUCode — код ККТУ всех креативов (A-42, Q-39); обязателен при настоящем адаптере.
	KKTUCode string `env:"ORD_KKTU_CODE"`
	// Реквизиты платформы-контрагента (R-5); с ordfake по умолчанию — значения разработки.
	PlatformINN       string `env:"ORD_PLATFORM_INN"`
	PlatformName      string `env:"ORD_PLATFORM_NAME"`
	PlatformLegalType string `env:"ORD_PLATFORM_LEGAL_TYPE" envDefault:"sole_proprietor"` // D-08
	// PlatformType — тип площадки канала MAX (A-32, Q-13).
	PlatformType string `env:"ORD_PLATFORM_TYPE" envDefault:"site"`
	// Параметры договоров (R-7, Q-14, A-33).
	InitialContractType   string `env:"ORD_INITIAL_CONTRACT_TYPE" envDefault:"intermediary-contract"`
	InitialSubjectType    string `env:"ORD_INITIAL_SUBJECT_TYPE" envDefault:"mediation"`
	InitialActionType     string `env:"ORD_INITIAL_ACTION_TYPE" envDefault:"conclude"`
	PublisherContractType string `env:"ORD_PUBLISHER_CONTRACT_TYPE" envDefault:"contract"`
	PublisherSubjectType  string `env:"ORD_PUBLISHER_SUBJECT_TYPE" envDefault:"distribution"`
	PublisherActionType   string `env:"ORD_PUBLISHER_ACTION_TYPE" envDefault:"distribution"`
	ContractNumberPrefix  string `env:"ORD_CONTRACT_NUMBER_PREFIX"`
	// Опрос статуса (05 §3.5, R-10).
	PollFastInterval time.Duration `env:"ORD_POLL_FAST_INTERVAL" envDefault:"1m"`
	PollFastPeriod   time.Duration `env:"ORD_POLL_FAST_PERIOD" envDefault:"10m"`
	PollSlowInterval time.Duration `env:"ORD_POLL_SLOW_INTERVAL" envDefault:"10m"`
	StatusTimeout    time.Duration `env:"ORD_STATUS_TIMEOUT" envDefault:"24h"`
	// Ожидание зависимостей (R-4).
	DependencyWait       time.Duration `env:"ORD_DEPENDENCY_WAIT" envDefault:"30s"`
	DependencyWaitFailed time.Duration `env:"ORD_DEPENDENCY_WAIT_FAILED" envDefault:"10m"`
	// EridWait — T-11: версия в approved без erid дольше — оповещение.
	EridWait time.Duration `env:"ORD_ERID_WAIT" envDefault:"1h"`
	// StuckSentAge — объект ОРД в sent дольше считается зависшим (07 §3).
	StuckSentAge time.Duration `env:"STUCK_ORD_SENT_AGE" envDefault:"24h"`
	// Ежемесячная отчётность (этап «API администратора и отчётность ОРД», R-18 … R-24, Q-43).
	InvoiceTypeInitial   string `env:"ORD_INVOICE_TYPE_INITIAL" envDefault:"intermediary-report"`
	InvoiceTypePublisher string `env:"ORD_INVOICE_TYPE_PUBLISHER" envDefault:"invoice"`
	InvoiceNumberPrefix  string `env:"ORD_INVOICE_NUMBER_PREFIX" envDefault:"MX"`
	// ReportAutoSend — задание 1-го числа отправляет отчёт сразу; иначе — дежурный после проверки.
	ReportAutoSend bool `env:"ORD_REPORT_AUTO_SEND" envDefault:"false"`
	// ReportErrorDay — с этого числа следующего месяца неотправленный отчёт — оповещение (R-23).
	ReportErrorDay int `env:"ORD_REPORT_ERROR_DAY" envDefault:"20"`
}

// Реквизиты платформы для разработки и тестов с ordfake (R-5): корректный ИНН ИП.
const (
	devPlatformINN  = "500100732259"
	devPlatformName = "ИП Платформа (разработка)"
)

var kktuRe = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}$`)

// invoicePrefixRe — префикс номера акта (ORD_INVOICE_NUMBER_PREFIX).
var invoicePrefixRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,16}$`)

// ORDFake сообщает, что вместо ОРД работает ordfake: в тестовом окружении всегда, в разработке —
// без OAuth-токена (R-18).
func (c Config) ORDFake() bool {
	return c.Env == EnvTest || (c.Env == EnvDevelopment && c.ORD.OAuthToken == "")
}

// ORDBaseURL — адрес ОРД: в development и test по умолчанию — тестовый контур (05 §3.8).
func (c Config) ORDBaseURL() string {
	if c.ORD.APIURL != "" {
		return c.ORD.APIURL
	}
	if c.Env == EnvProduction {
		return ""
	}
	return ord.TestBaseURL
}

// ORDPlatform — реквизиты платформы с подстановкой значений разработки при ordfake.
func (c Config) ORDPlatform() (inn, name string, legalType accountdomain.LegalType) {
	inn, name, legalType = c.ORD.PlatformINN, c.ORD.PlatformName, accountdomain.LegalType(c.ORD.PlatformLegalType)
	if c.ORDFake() {
		if inn == "" {
			inn, legalType = devPlatformINN, accountdomain.SoleProprietor
		}
		if name == "" {
			name = devPlatformName
		}
	}
	return inn, name, legalType
}

// MarkingConfig — параметры модуля маркировки.
func (c Config) MarkingConfig() markingdomain.Config {
	o := c.ORD
	return markingdomain.Config{
		KKTUCode: o.KKTUCode, PlatformType: o.PlatformType,
		Contracts: markingdomain.ContractRules{
			Initial:      markingdomain.ContractParams{Type: o.InitialContractType, SubjectType: o.InitialSubjectType, ActionType: o.InitialActionType},
			Publisher:    markingdomain.ContractParams{Type: o.PublisherContractType, SubjectType: o.PublisherSubjectType, ActionType: o.PublisherActionType},
			NumberPrefix: o.ContractNumberPrefix,
		},
		Poll: markingdomain.PollRules{FastInterval: o.PollFastInterval, FastPeriod: o.PollFastPeriod,
			SlowInterval: o.PollSlowInterval, Timeout: o.StatusTimeout},
		DependencyWait: o.DependencyWait, DependencyWaitFailed: o.DependencyWaitFailed,
		EridWait: o.EridWait, StuckSentAge: o.StuckSentAge,
		Report: markingdomain.ReportRules{InvoiceTypeInitial: o.InvoiceTypeInitial, InvoiceTypePublisher: o.InvoiceTypePublisher,
			NumberPrefix: o.InvoiceNumberPrefix, AutoSend: o.ReportAutoSend, ErrorDay: o.ReportErrorDay},
	}
}

// validateORD — параметры ОРД режимов api и worker (07 §8, R-5, R-7, R-18).
func (c Config) validateORD(add func(field, reason string)) {
	o := c.ORD
	if c.Env == EnvProduction && !o.Enabled {
		add("ORD_ENABLED", "cannot be false in production")
	}
	if !o.Enabled {
		return
	}
	fake := c.ORDFake()
	if c.Env == EnvProduction {
		if u, err := url.Parse(o.APIURL); err != nil || u.Scheme != "https" || u.Host == "" {
			add("ORD_API_URL", "must be an https URL in production")
		}
	} else if o.APIURL != "" {
		if u, err := url.Parse(o.APIURL); err != nil || u.Scheme == "" || u.Host == "" {
			add("ORD_API_URL", "must be an absolute URL")
		}
	}
	if !fake && strings.TrimSpace(o.OAuthToken) == "" {
		add("ORD_OAUTH_TOKEN", "required outside development and test")
	}
	if strings.TrimSpace(o.APIVersion) == "" {
		add("ORD_API_VERSION", "required")
	}
	if o.Rate <= 0 {
		add("ORD_RATE", "must be positive")
	}
	switch {
	case o.KKTUCode == "" && !fake:
		add("ORD_KKTU_CODE", "required with the real ORD adapter")
	case o.KKTUCode != "" && !kktuRe.MatchString(o.KKTUCode):
		add("ORD_KKTU_CODE", "must look like 30.15.1")
	}
	inn, name, legal := c.ORDPlatform()
	if !legal.Valid() {
		add("ORD_PLATFORM_LEGAL_TYPE", "must be legal_entity, sole_proprietor or self_employed")
	} else if inn == "" {
		add("ORD_PLATFORM_INN", "required")
	} else if reason := accountdomain.CheckINN(inn, legal); reason != "" {
		add("ORD_PLATFORM_INN", reason)
	}
	if strings.TrimSpace(name) == "" {
		add("ORD_PLATFORM_NAME", "required")
	}
	if !slices.Contains(markingdomain.OrdPlatformTypes, o.PlatformType) {
		add("ORD_PLATFORM_TYPE", "must be one of "+strings.Join(markingdomain.OrdPlatformTypes, ", "))
	}
	for _, e := range []struct {
		field, value string
		allowed      []string
	}{
		{"ORD_INITIAL_CONTRACT_TYPE", o.InitialContractType, markingdomain.OrdContractTypes},
		{"ORD_INITIAL_SUBJECT_TYPE", o.InitialSubjectType, markingdomain.OrdSubjectTypes},
		{"ORD_INITIAL_ACTION_TYPE", o.InitialActionType, markingdomain.OrdActionTypes},
		{"ORD_PUBLISHER_CONTRACT_TYPE", o.PublisherContractType, markingdomain.OrdContractTypes},
		{"ORD_PUBLISHER_SUBJECT_TYPE", o.PublisherSubjectType, markingdomain.OrdSubjectTypes},
		{"ORD_PUBLISHER_ACTION_TYPE", o.PublisherActionType, markingdomain.OrdActionTypes},
		{"ORD_INVOICE_TYPE_INITIAL", o.InvoiceTypeInitial, markingdomain.OrdInvoiceTypes},
		{"ORD_INVOICE_TYPE_PUBLISHER", o.InvoiceTypePublisher, markingdomain.OrdInvoiceTypes},
	} {
		if !slices.Contains(e.allowed, e.value) {
			add(e.field, "must be one of "+strings.Join(e.allowed, ", "))
		}
	}
	for _, d := range []struct {
		field string
		value time.Duration
	}{
		{"ORD_POLL_FAST_INTERVAL", o.PollFastInterval}, {"ORD_POLL_FAST_PERIOD", o.PollFastPeriod},
		{"ORD_POLL_SLOW_INTERVAL", o.PollSlowInterval}, {"ORD_STATUS_TIMEOUT", o.StatusTimeout},
		{"ORD_DEPENDENCY_WAIT", o.DependencyWait}, {"ORD_DEPENDENCY_WAIT_FAILED", o.DependencyWaitFailed},
		{"ORD_ERID_WAIT", o.EridWait}, {"STUCK_ORD_SENT_AGE", o.StuckSentAge},
	} {
		if d.value <= 0 {
			add(d.field, "must be positive")
		}
	}
	if !invoicePrefixRe.MatchString(o.InvoiceNumberPrefix) {
		add("ORD_INVOICE_NUMBER_PREFIX", "must be 1-16 latin letters, digits or dashes")
	}
	if o.ReportErrorDay < 1 || o.ReportErrorDay > 28 {
		add("ORD_REPORT_ERROR_DAY", "must be 1..28")
	}
	if o.PollFastInterval >= o.StatusTimeout || o.PollSlowInterval >= o.StatusTimeout || o.PollFastPeriod >= o.StatusTimeout {
		add("ORD_POLL_*", "must be less than ORD_STATUS_TIMEOUT")
	}
}
