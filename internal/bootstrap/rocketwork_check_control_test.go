package bootstrap

import (
	"bytes"
	"context"
	"testing"
)

func TestDiagnosticCLIRejectsDevApproval(t *testing.T) {
	for _, args := range [][]string{{"issue", "--email", "senior@example.test"}, {"execute", "--approved-manifest-sha256", "forged"}, {"execute", "--run", "forged"}, {"issue", "--grant", "forged"}, {"withdraw"}, {"reconcile-withdrawal"}, {"refund"}, {"invite"}, {"subscriptions"}} {
		var output bytes.Buffer
		p := RunParams{Command: "rocketwork-check", Args: args, Stdout: &output, Stderr: &output, Environ: []string{"APP_ENV=development", "ROCKETWORK_ACCESS_MODE=controlled", "ROCKETWORK_PRODUCTION_APPROVED=true"}}
		if Run(context.Background(), p) == 0 {
			t.Fatalf("dev flags authorized %v", args)
		}
	}
}
