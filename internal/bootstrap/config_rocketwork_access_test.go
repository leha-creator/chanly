package bootstrap

import (
	"strings"
	"testing"

	checkdomain "github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

func TestRocketWorkAccessMatrix(t *testing.T) {
	for _, environment := range []string{EnvDevelopment, EnvTest, EnvProduction} {
		for _, access := range []string{"", "readonly", "controlled", "automation", "invalid"} {
			for _, target := range []string{"production", "local-emulator", "invalid"} {
				t.Run(environment+"/"+access+"/"+target, func(t *testing.T) {
					c := Config{Env: environment, RocketWork: RocketWorkConfig{
						Mode: RocketWorkLive, AccessMode: access, Purpose: checkdomain.Business,
						Target: target, ProductionApproved: true, WorkflowEnabled: true, AccountKey: "test",
					}}
					if target == "local-emulator" {
						c.RocketWork.BaseURL = "http://127.0.0.1:19001"
					}
					want := access == "automation" && ((environment == EnvProduction && target == "production") || (environment != EnvProduction && target == "local-emulator"))
					if got := c.PayoutAppConfig().ProviderScope != nil; got != want {
						t.Fatalf("scope enabled=%v, want %v", got, want)
					}
					var denied bool
					c.validateRocketWork(func(field, _ string) {
						if field == "ROCKETWORK_ACCESS_MODE" || field == "ROCKETWORK_WORKFLOW_ENABLED" {
							denied = true
						}
					})
					if denied == want {
						t.Fatalf("access denial=%v, want %v", denied, !want)
					}
				})
			}
		}
	}
}

func TestRocketWorkLiveRequiresExplicitTarget(t *testing.T) {
	c := Config{RocketWork: RocketWorkConfig{Mode: RocketWorkLive}}
	var denied bool
	c.validateRocketWork(func(field, _ string) {
		if field == "ROCKETWORK_TARGET" {
			denied = true
		}
	})
	if !denied || c.RocketWorkAccess() != checkdomain.ReadOnly {
		t.Fatal("missing target or default capability accepted")
	}
}

func TestRocketWorkDevelopmentLiveAdmission(t *testing.T) {
	valid := func() map[string]string {
		vars := validWorkerEnv()
		for key, value := range map[string]string{
			"ROCKETWORK_MODE": "live", "ROCKETWORK_TARGET": "production",
			"ROCKETWORK_BASE_URL": "https://api.rocketwork.ru", "ROCKETWORK_TOKEN": "synthetic-live-token",
			"ROCKETWORK_WEBHOOK_SECRET": "synthetic-webhook-secret-012345",
			"ROCKETWORK_ACCESS_MODE":    "automation", "ROCKETWORK_PURPOSE": "business",
			"ROCKETWORK_PRODUCTION_APPROVED": "true", "ROCKETWORK_DEV_LIVE_APPROVED": "true",
			"ROCKETWORK_WORKFLOW_ENABLED": "true", "ROCKETWORK_ACCOUNT_KEY": "synthetic-live-account",
			"ROCKETWORK_ACT_REQUIRED": "true", "ROCKETWORK_TAX_RETENTION": "false",
			"ROCKETWORK_MAX_COMMISSION_KOPECKS": "100", "ROCKETWORK_CONTRACT_MODE": "online",
			"ROCKETWORK_DOCUMENT_NAME": "Synthetic agreement", "ROCKETWORK_TASK_TITLE": "Synthetic service",
			"ROCKETWORK_TASK_DESCRIPTION": "Synthetic description",
			"ROCKETWORK_PERIOD_FROM":      "2026-11-01", "ROCKETWORK_PERIOD_TO": "2026-11-30",
		} {
			vars[key] = value
		}
		return vars
	}
	for _, runtime := range []string{ModeAPI, ModeWorker} {
		cfg, err := LoadConfig(valid(), runtime)
		if err != nil {
			t.Fatalf("%s: %v", runtime, err)
		}
		if !cfg.rocketWorkAutomationAllowed() || cfg.PayoutAppConfig().ProviderScope == nil {
			t.Fatal("explicit development admission did not enable business workflow")
		}
		if strings.Contains(cfg.LogValue().String(), "synthetic-live-token") {
			t.Fatal("provider token logged")
		}
	}
	for key, value := range map[string]string{
		"APP_ENV": EnvTest, "ROCKETWORK_DEV_LIVE_APPROVED": "false", "ROCKETWORK_PRODUCTION_APPROVED": "false",
		"ROCKETWORK_WORKFLOW_ENABLED": "false", "ROCKETWORK_ACCESS_MODE": "readonly",
		"ROCKETWORK_PURPOSE": "diagnostic", "ROCKETWORK_MODE": "fake", "ROCKETWORK_TARGET": "invalid",
		"ROCKETWORK_ACCOUNT_KEY": "", "ROCKETWORK_MAX_COMMISSION_KOPECKS": "",
	} {
		t.Run(key, func(t *testing.T) {
			vars := valid()
			vars[key] = value
			if _, err := LoadConfig(vars, ModeWorker); err == nil {
				t.Fatal("incomplete development admission accepted")
			}
		})
	}
	t.Run("explicit unlimited commission", func(t *testing.T) {
		vars := valid()
		delete(vars, "ROCKETWORK_MAX_COMMISSION_KOPECKS")
		vars["ROCKETWORK_COMMISSION_UNLIMITED"] = "true"
		cfg, err := LoadConfig(vars, ModeWorker)
		if err != nil || cfg.PayoutAppConfig().AgreementTemplate == nil || !cfg.PayoutAppConfig().AgreementTemplate.CommissionUnlimited {
			t.Fatalf("explicit unlimited policy was not preserved: %v", err)
		}
		vars["ROCKETWORK_MAX_COMMISSION_KOPECKS"] = "0"
		if _, err := LoadConfig(vars, ModeWorker); err == nil {
			t.Fatal("conflicting commission policies accepted")
		}
	})
}

func TestOrdinaryRuntimeRejectsControlSecrets(t *testing.T) {
	for _, key := range []string{"ROCKETWORK_CONTROL_DATABASE_URL", "ROCKETWORK_POLICY_DATABASE_URL", "ROCKETWORK_CHECK_ARMING_ID"} {
		vars := validWorkerEnv()
		vars[key] = "synthetic-private-value"
		cfg, err := LoadConfig(vars, ModeWorker)
		if err == nil {
			t.Fatalf("accepted %s", key)
		}
		if strings.Contains(cfg.LogValue().String(), "synthetic-private-value") {
			t.Fatal("secret logged")
		}
		for _, runtime := range []string{ModeAdmin, ModeDev} {
			if _, err := LoadConfig(vars, runtime); err == nil {
				t.Fatalf("%s accepted %s", runtime, key)
			}
		}
	}
	for _, mode := range []string{RocketWorkLive, RocketWorkFake, RocketWorkDisabled} {
		vars := validWorkerEnv()
		vars["ROCKETWORK_MODE"] = mode
		vars["ROCKETWORK_TARGET"] = "production"
		vars["ROCKETWORK_TOKEN"] = "synthetic-private-token"
		if _, err := LoadConfig(vars, ModeWorker); err == nil {
			t.Fatalf("dev primary token accepted in %s", mode)
		}
	}
}
