package bootstrap

import (
	"testing"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/tbank"
)

func TestBankInvoiceProviderConfig(t *testing.T) {
	cfg, err := LoadConfig(validAPIEnv(), ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Billing.InvoiceProvider != "local" {
		t.Fatal("bank issuance must be opt-in")
	}
	cfg.TBank.Mode = "live"
	cfg.TBank.BaseURL = tbank.SandboxURL
	cfg.TBank.Token = "test-token"
	cfg.TBank.AccessMode = domain.BankAutomation
	cfg.Billing.InvoiceProvider = "tbank"
	var errors []string
	check := func() {
		errors = nil
		cfg.validateBilling(func(field, reason string) { errors = append(errors, field+": "+reason) })
	}
	check()
	if len(errors) != 0 {
		t.Fatalf("valid sandbox provider: %v", errors)
	}
	cfg.TBank.AccessMode = domain.BankReadOnly
	check()
	if len(errors) == 0 {
		t.Fatal("read-only provider accepted")
	}
	cfg.TBank.AccessMode = domain.BankAutomation
	cfg.Billing.InvoiceVATNote = "НДС 20%"
	check()
	if len(errors) == 0 {
		t.Fatal("unsupported VAT accepted")
	}
}
