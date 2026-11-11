package bootstrap

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBillingConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Billing
	if b.InvoiceTTL != 336*time.Hour || b.InvoiceVATNote != "Без НДС" || b.InvoiceRatePerUser != 10 || b.InvoiceRatePerIP != 30 ||
		b.InvoiceExpireInterval != time.Hour || b.StatementMaxBytes != 10<<20 || b.LedgerCheckInterval != time.Hour ||
		b.ManualSecondApproval != 10_000_000 || b.ClosingActsDay != 1 || b.ClosingActsTime != "03:00" {
		t.Fatalf("billing defaults: %+v", b)
	}
	// Вне production — реквизиты разработки с верными ключами счетов.
	s := cfg.BillingSupplier()
	if s.BankAccount != devPlatformBankAccount || s.BankBIK != devPlatformBankBIK || s.INN == "" || s.OGRNIP == "" {
		t.Fatalf("dev supplier: %+v", s)
	}
	if !accountKeyValid(s.BankBIK[6:], s.BankAccount) || !accountKeyValid("0"+s.BankBIK[4:6], s.BankCorrAccount) {
		t.Fatal("dev accounts have invalid keys")
	}
	if h, m := cfg.ClosingActsAt(); h != 3 || m != 0 {
		t.Fatalf("closing acts at %d:%02d", h, m)
	}
	logged := cfg.LogValue().String()
	if strings.Contains(logged, devPlatformBankAccount) || !strings.Contains(logged, "0001") || !strings.Contains(logged, "invoice_ttl") {
		t.Fatalf("billing config log: %s", logged)
	}

	for _, tc := range []struct {
		key, value, field string
		ok                bool
	}{
		{"BILLING_INVOICE_PROVIDER", "unexpected", "BILLING_INVOICE_PROVIDER", false},
		{"BILLING_INVOICE_PROVIDER", "local", "", true},
		{"BILLING_INVOICE_PROVIDER", "tbank", "BILLING_INVOICE_PROVIDER", false},
		{"CLOSING_ACTS_DAY", "0", "CLOSING_ACTS_DAY", false},
		{"CLOSING_ACTS_DAY", "1", "", true},
		{"CLOSING_ACTS_DAY", "28", "", true},
		{"CLOSING_ACTS_DAY", "29", "CLOSING_ACTS_DAY", false},
		{"CLOSING_ACTS_TIME", "23:59", "", true},
		{"CLOSING_ACTS_TIME", "24:00", "CLOSING_ACTS_TIME", false},
		{"CLOSING_ACTS_TIME", "3:00", "CLOSING_ACTS_TIME", false},
		{"INVOICE_TTL", "0s", "INVOICE_TTL", false},
		{"INVOICE_TTL", "1m", "", true},
		{"LEDGER_CHECK_INTERVAL", "-1h", "INVOICE_TTL", false},
		{"INVOICE_RATE_PER_USER", "0", "INVOICE_RATE_PER_USER", false},
		{"INVOICE_RATE_PER_IP", "1", "", true},
		{"STATEMENT_MAX_BYTES", "0", "STATEMENT_MAX_BYTES", false},
		{"LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD", "0", "LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD", false},
		{"LEDGER_MANUAL_SECOND_APPROVAL_THRESHOLD", "1", "", true},
		{"PLATFORM_BANK_BIK", "04452522", "PLATFORM_BANK_BIK", false},
		{"PLATFORM_BANK_ACCOUNT", "40802810838000000002", "PLATFORM_BANK_ACCOUNT", false},
		{"PLATFORM_BANK_ACCOUNT", "4080281083800000000", "PLATFORM_BANK_ACCOUNT", false},
		{"PLATFORM_BANK_CORR_ACCOUNT", "30101810400000000226", "PLATFORM_BANK_CORR_ACCOUNT", false},
	} {
		env := validAPIEnv()
		env[tc.key] = tc.value
		_, err := LoadConfig(env, ModeAPI)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		case !tc.ok && (err == nil || !slices.ContainsFunc(configFields(t, err), func(f string) bool { return strings.HasPrefix(f, tc.field) })):
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
	}

	// production: реквизиты платформы обязательны.
	prod := validWorkerEnv()
	prod["APP_ENV"] = "production"
	_, err = LoadConfig(prod, ModeWorker)
	if err == nil {
		t.Fatal("production without platform requisites accepted")
	}
	fields := configFields(t, err)
	for _, want := range []string{"PLATFORM_OGRNIP", "PLATFORM_ADDRESS", "PLATFORM_BANK_ACCOUNT", "PLATFORM_BANK_NAME",
		"PLATFORM_BANK_BIK", "PLATFORM_BANK_CORR_ACCOUNT"} {
		if !slices.ContainsFunc(fields, func(f string) bool { return strings.HasPrefix(f, want+":") }) {
			t.Errorf("production without %s accepted: %v", want, fields)
		}
	}
	// Служебные команды денег проверяют только базу.
	if _, err := LoadConfig(map[string]string{"DATABASE_URL": "postgres://x"}, ModeAdmin); err != nil {
		t.Errorf("admin mode needs only the database: %v", err)
	}
}
