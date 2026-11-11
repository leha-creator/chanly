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
