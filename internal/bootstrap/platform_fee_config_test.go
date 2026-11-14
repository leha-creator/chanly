package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPlatformFeeConfigDefaultAndOverrides(t *testing.T) {
	for _, mode := range []string{ModeAPI, ModeWorker} {
		for _, value := range []string{"", "1500", "2500", "0", "10000", "-1", "10001", "invalid"} {
			env := validAPIEnv()
			if mode == ModeWorker {
				env = validWorkerEnv()
			}
			if value != "" {
				env["PLATFORM_FEE_RATE_BP"] = value
			}
			cfg, err := LoadConfig(env, mode)
			invalid := value == "-1" || value == "10001" || value == "invalid"
			if invalid {
				if err == nil {
					t.Errorf("mode=%v value=%s accepted", mode, value)
				}
				continue
			}
			want := 2500
			switch value {
			case "1500":
				want = 1500
			case "0":
				want = 0
			case "10000":
				want = 10000
			}
			if err != nil || cfg.Channel.FeeRateBP != want {
				t.Fatalf("mode=%v value=%s: rate=%d err=%v", mode, value, cfg.Channel.FeeRateBP, err)
			}
		}
	}
}

func TestPlatformFeeConfigLogsAtInfoWithoutSecrets(t *testing.T) {
	env := validAPIEnv()
	env["LOG_LEVEL"] = "info"
	var out, errOut bytes.Buffer
	code := Run(context.Background(), RunParams{Command: ModeConfig, Args: []string{"check", "--for", "api"}, Environ: environ(env), Stdout: &out, Stderr: &errOut})
	text := out.String()
	if code != 0 || !strings.Contains(text, `"msg":"platform fee configured"`) || !strings.Contains(text, `"platform_fee_rate_bp":2500`) {
		t.Fatalf("config log: code=%d %s %s", code, text, errOut.String())
	}
	if strings.Contains(text, env["DATABASE_URL"]) || strings.Contains(text, env["AUTH_CODE_PEPPER"]) {
		t.Fatal("configuration log contains private fields")
	}
}
