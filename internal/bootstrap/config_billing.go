package bootstrap

import (
	"fmt"
	"regexp"
	"time"

	billingapp "github.com/leha-creator/chanly/internal/billing/app"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
)

// BillingConfig — приём оплаты и учёт денег (этап «Приём оплаты и учёт денег», R-10 … R-16).
type BillingConfig struct {
	// Реквизиты платформы-получателя для счетов и закрывающих документов (R-10); наименование
	// и ИНН — ORD_PLATFORM_*. Обязательны в production; в development и test — значения разработки.
	PlatformOGRNIP          string `env:"PLATFORM_OGRNIP"`
	PlatformAddress         string `env:"PLATFORM_ADDRESS"`
	PlatformBankAccount     string `env:"PLATFORM_BANK_ACCOUNT"`
	PlatformBankName        string `env:"PLATFORM_BANK_NAME"`
	PlatformBankBIK         string `env:"PLATFORM_BANK_BIK"`
	PlatformBankCorrAccount string `env:"PLATFORM_BANK_CORR_ACCOUNT"`

	InvoiceTTL            time.Duration `env:"INVOICE_TTL" envDefault:"336h"`
	InvoiceVATNote        string        `env:"INVOICE_VAT_NOTE" envDefault:"Без НДС"` // Q-32
	InvoiceRatePerUser    int           `env:"INVOICE_RATE_PER_USER" envDefault:"10"`
	InvoiceRatePerIP      int           `env:"INVOICE_RATE_PER_IP" envDefault:"30"`
	InvoiceExpireInterval time.Duration `env:"INVOICE_EXPIRE_INTERVAL" envDefault:"1h"`
	StatementMaxBytes     int           `env:"STATEMENT_MAX_BYTES" envDefault:"10485760"`
	// LedgerCheckInterval — период check_ledger_balance (R-15).
	LedgerCheckInterval time.Duration `env:"LEDGER_CHECK_INTERVAL" envDefault:"1h"`
	// ManualSecondApproval — порог ручной проводки, выше которого нужен второй администратор
	// (R-14), в копейках.
	ManualSecondApproval int64 `env:"LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD" envDefault:"10000000"`
	// ApprovalTTL — срок запроса второго подтверждения через HTTP (R-2 этапа «API администратора»).
	ApprovalTTL time.Duration `env:"ADMIN_APPROVAL_TTL" envDefault:"72h"`
	// ClosingActsDay, ClosingActsTime — запуск generate_closing_acts по Москве (R-16).
	ClosingActsDay  int    `env:"CLOSING_ACTS_DAY" envDefault:"1"`
	ClosingActsTime string `env:"CLOSING_ACTS_TIME" envDefault:"03:00"`
}

// Реквизиты платформы для разработки и тестов: БИК и корр. счёт ПАО Сбербанк, счёт с верным
// контрольным ключом.
const (
	devPlatformOGRNIP      = "320774600000001"
	devPlatformAddress     = "г. Москва (разработка)"
	devPlatformBankAccount = "40802810838000000001"
	devPlatformBankName    = "ПАО СБЕРБАНК"
	devPlatformBankBIK     = "044525225"
	devPlatformCorrAccount = "30101810400000000225"
)

var (
	bikRe     = regexp.MustCompile(`^\d{9}$`)
	accountRe = regexp.MustCompile(`^\d{20}$`)
	clockRe   = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
)

// accountKeyValid — контрольный ключ счёта по БИК (положение Банка России № 579-П): весовые
// коэффициенты 7, 1, 3 к 23 цифрам; сумма младших разрядов произведений кратна 10. Для
// расчётного счёта перед ним — три последние цифры БИК, для корр. счёта — «0» и 5–6 цифры БИК.
func accountKeyValid(prefix, account string) bool {
	digits := prefix + account
	if len(digits) != 23 {
		return false
	}
	weights := [3]int{7, 1, 3}
	sum := 0
	for i, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
		sum += (int(c-'0') * weights[i%3]) % 10
	}
	return sum%10 == 0
}

// BillingSupplier — реквизиты платформы с подстановкой значений разработки вне production.
func (c Config) BillingSupplier() billingdomain.Supplier {
	inn, name, _ := c.ORDPlatform()
	if inn == "" && c.Env != EnvProduction {
		inn, name = devPlatformINN, devPlatformName
	}
	b := c.Billing
	s := billingdomain.Supplier{
		Name: name, INN: inn, OGRNIP: b.PlatformOGRNIP, Address: b.PlatformAddress, BankAccount: b.PlatformBankAccount,
		BankName: b.PlatformBankName, BankBIK: b.PlatformBankBIK, BankCorrAccount: b.PlatformBankCorrAccount,
	}
	if c.Env != EnvProduction {
		def := func(v *string, d string) {
			if *v == "" {
				*v = d
			}
		}
		def(&s.OGRNIP, devPlatformOGRNIP)
		def(&s.Address, devPlatformAddress)
		def(&s.BankAccount, devPlatformBankAccount)
		def(&s.BankName, devPlatformBankName)
		def(&s.BankBIK, devPlatformBankBIK)
		def(&s.BankCorrAccount, devPlatformCorrAccount)
	}
	return s
}

// BillingAppConfig — параметры модуля расчётов.
func (c Config) BillingAppConfig() billingapp.Config {
	b := c.Billing
	return billingapp.Config{
		InvoiceTTL: b.InvoiceTTL, InvoicesPerUserHour: b.InvoiceRatePerUser, InvoicesPerIPHour: b.InvoiceRatePerIP,
		Supplier: c.BillingSupplier(), VATNote: b.InvoiceVATNote,
	}
}

// ClosingActsAt — час и минута запуска generate_closing_acts.
func (c Config) ClosingActsAt() (hour, minute int) {
	if m := clockRe.FindStringSubmatch(c.Billing.ClosingActsTime); m != nil {
		fmt.Sscanf(m[1]+" "+m[2], "%d %d", &hour, &minute)
	}
	return hour, minute
}

// StatementMaxBytes — предел размера файла выписки.
func (c Config) StatementMaxBytes() int {
	if c.Billing.StatementMaxBytes <= 0 {
		return clientbank.DefaultMaxBytes
	}
	return c.Billing.StatementMaxBytes
}

// validateBilling — реквизиты платформы, сроки и лимиты (R-10, R-11, R-14 … R-16).
func (c Config) validateBilling(add func(field, reason string)) {
	b := c.Billing
	if c.Env == EnvProduction {
		for field, v := range map[string]string{
			"PLATFORM_OGRNIP": b.PlatformOGRNIP, "PLATFORM_ADDRESS": b.PlatformAddress,
			"PLATFORM_BANK_ACCOUNT": b.PlatformBankAccount, "PLATFORM_BANK_NAME": b.PlatformBankName,
			"PLATFORM_BANK_BIK": b.PlatformBankBIK, "PLATFORM_BANK_CORR_ACCOUNT": b.PlatformBankCorrAccount,
		} {
			if v == "" {
				add(field, "required in production")
			}
		}
	}
	s := c.BillingSupplier()
	switch {
	case s.BankBIK != "" && !bikRe.MatchString(s.BankBIK):
		add("PLATFORM_BANK_BIK", "must be 9 digits")
	case s.BankBIK != "":
		if s.BankAccount != "" && (!accountRe.MatchString(s.BankAccount) || !accountKeyValid(s.BankBIK[6:], s.BankAccount)) {
			add("PLATFORM_BANK_ACCOUNT", "must be 20 digits with a valid key for the BIK")
		}
		if s.BankCorrAccount != "" && (!accountRe.MatchString(s.BankCorrAccount) || !accountKeyValid("0"+s.BankBIK[4:6], s.BankCorrAccount)) {
			add("PLATFORM_BANK_CORR_ACCOUNT", "must be 20 digits with a valid key for the BIK")
		}
	}
	if b.InvoiceTTL <= 0 || b.InvoiceExpireInterval <= 0 || b.LedgerCheckInterval <= 0 {
		add("INVOICE_TTL, INVOICE_EXPIRE_INTERVAL, LEDGER_CHECK_INTERVAL", "must be positive")
	}
	if b.InvoiceRatePerUser < 1 || b.InvoiceRatePerIP < 1 {
		add("INVOICE_RATE_PER_USER, INVOICE_RATE_PER_IP", "must be positive")
	}
	if b.StatementMaxBytes < 1 {
		add("STATEMENT_MAX_BYTES", "must be positive")
	}
	if b.ManualSecondApproval < 1 {
		add("LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD", "must be positive")
	}
	if b.ApprovalTTL < time.Hour || b.ApprovalTTL > 30*24*time.Hour {
		add("ADMIN_APPROVAL_TTL", "must be between 1h and 720h")
	}
	if b.ClosingActsDay < 1 || b.ClosingActsDay > 28 {
		add("CLOSING_ACTS_DAY", "must be 1..28")
	}
	if !clockRe.MatchString(b.ClosingActsTime) {
		add("CLOSING_ACTS_TIME", "must be HH:MM")
	}
}
