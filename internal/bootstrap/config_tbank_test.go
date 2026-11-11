package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/tbank"
)

func TestTBankConfigSafety(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]string
		bad  bool
	}{
		{"default disabled", nil, false},
		{"token alone stays disabled", map[string]string{"TBANK_TOKEN": "private"}, false},
		{"live missing parameters", map[string]string{"TBANK_MODE": "live"}, true},
		{"insecure url", map[string]string{"TBANK_MODE": "live", "TBANK_API_URL": "http://business.tbank.ru/openapi/sandbox"}, true},
		{"unknown mode", map[string]string{"TBANK_MODE": "auto"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := validAPIEnv()
			for k, v := range tc.set {
				vars[k] = v
			}
			cfg, err := LoadConfig(vars, ModeAPI)
			if (err != nil) != tc.bad {
				t.Fatalf("config error %v", err)
			}
			if err == nil && cfg.TBank.Mode != "disabled" {
				t.Fatalf("unexpected mode %s", cfg.TBank.Mode)
			}
		})
	}
}

func TestTBankRealAccessMatrix(t *testing.T) {
	vars := validAPIEnv()
	vars["TBANK_MODE"] = "live"
	vars["TBANK_API_URL"] = tbank.ProductionURL
	vars["TBANK_DEV_REAL_ENABLED"] = "true"
	vars["TBANK_TOKEN"] = "private-read-token"
	vars["TBANK_IMPORT_FROM"] = "2026-10-01T00:00:00Z"
	vars["PLATFORM_BANK_ACCOUNT"] = devPlatformBankAccount
	base, err := LoadConfig(vars, ModeAPI)
	if err != nil {
		t.Fatal(err)
	}
	base.TBank.DevPaymentCode = "dev-server"
	base.ORD.PlatformINN = devPlatformINN
	base.ORD.PlatformName = devPlatformName
	cases := []struct {
		name          string
		edit          func(*Config)
		invalid, auto bool
	}{
		{"readonly default", func(c *Config) {}, false, false},
		{"disabled is a kill switch", func(c *Config) {
			c.TBank.Mode = "disabled"
			c.TBank.AccessMode = domain.BankControlled
			c.TBank.DevRealEnabled = false
			c.TBank.DevPaymentCode = ""
		}, false, false},
		{"dev gate off", func(c *Config) { c.TBank.DevRealEnabled = false }, true, false},
		{"controlled", func(c *Config) { c.TBank.AccessMode = domain.BankControlled }, false, true},
		{"missing supplier", func(c *Config) { c.TBank.AccessMode = domain.BankControlled; c.ORD.PlatformINN = "" }, true, false},
		{"missing code", func(c *Config) { c.TBank.AccessMode = domain.BankControlled; c.TBank.DevPaymentCode = "" }, true, false},
		{"invalid code", func(c *Config) { c.TBank.AccessMode = domain.BankControlled; c.TBank.DevPaymentCode = "bad code" }, true, false},
		{"dev unrestricted denied", func(c *Config) { c.TBank.AccessMode = domain.BankAutomation; c.TBank.ProductionApproved = true }, true, false},
		{"test real denied", func(c *Config) { c.Env = EnvTest; c.TBank.AccessMode = domain.BankControlled }, true, false},
		{"production controlled denied", func(c *Config) { c.Env = EnvProduction; c.TBank.AccessMode = domain.BankControlled }, true, false},
		{"production automation approval required", func(c *Config) { c.Env = EnvProduction; c.TBank.AccessMode = domain.BankAutomation }, true, false},
		{"production approved", func(c *Config) {
			c.Env = EnvProduction
			c.TBank.AccessMode = domain.BankAutomation
			c.TBank.ProductionApproved = true
		}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.edit(&c)
			var bad []string
			c.validateTBank(func(f, _ string) { bad = append(bad, f) })
			if (len(bad) > 0) != tc.invalid || c.bankAutoSyncEnabled() != tc.auto {
				t.Fatalf("fields=%v background=%v", bad, c.bankAutoSyncEnabled())
			}
		})
	}
}

func TestTBankRealPreflightRequiresExplicitTarget(t *testing.T) {
	vars := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unreachable.invalid/tbank_check", "INTEGRATION_CHECK_DATABASE_URL": "postgres://unreachable.invalid/tbank_check", "TBANK_MODE": "live", "TBANK_API_URL": tbank.ProductionURL, "TBANK_DEV_REAL_ENABLED": "true", "TBANK_TOKEN": "read-token", "TBANK_IMPORT_FROM": "2026-10-01T00:00:00Z", "PLATFORM_BANK_ACCOUNT": devPlatformBankAccount, "INTEGRATION_CHECK_TBANK_ACCOUNT": devPlatformBankAccount}
	c, err := LoadConfig(vars, ModeDev)
	if err != nil {
		t.Fatal(err)
	}
	o := integrationCheckOptions{system: "tbank", scenario: "preflight"}
	if err := integrationCheckPreflight(c, vars, &o); err == nil {
		t.Fatal("implicit production endpoint accepted")
	}
	o.target = "production"
	if err := integrationCheckPreflight(c, vars, &o); err != nil {
		t.Fatal(err)
	}
	o.scenario = "sync"
	o.execute = true
	o.runDir = t.TempDir()
	if err := integrationCheckPreflight(c, vars, &o); err == nil {
		t.Fatal("readonly permitted sync")
	}
}
func TestTBankPreflightNeverUsesNetwork(t *testing.T) {
	vars := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unreachable.invalid/tbank_check", "INTEGRATION_CHECK_DATABASE_URL": "postgres://unreachable.invalid/tbank_check", "TBANK_MODE": "live", "TBANK_TOKEN": "private-secret-marker", "TBANK_IMPORT_FROM": "2026-10-01T00:00:00Z", "PLATFORM_BANK_ACCOUNT": "40702810110011000000", "INTEGRATION_CHECK_TBANK_ACCOUNT": "40702810110011000000"}
	var out bytes.Buffer
	p := RunParams{Command: "integration-check", Args: []string{"--system", "tbank"}, Environ: environ(vars), Stdout: &out, Stderr: &out}
	if code := Run(context.Background(), p); code != 0 {
		t.Fatalf("preflight %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"network_called":false`) || strings.Contains(out.String(), "private-secret-marker") {
		t.Fatalf("unsafe preflight %s", out.String())
	}
	for _, env := range []string{"test", "production"} {
		vars["APP_ENV"] = env
		p.Environ = environ(vars)
		if code := Run(context.Background(), p); code == 0 {
			t.Fatalf("accepted %s", env)
		}
	}
}
