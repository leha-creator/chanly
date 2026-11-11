package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworktest"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func rwManifestFixture(origin string) rwCheckManifest {
	id := uuid.NewString()
	m := rwCheckManifest{Version: 1, RunID: id, Target: "local-emulator", Origin: origin, AccountKey: "synthetic", CompanyINN: "000000000000", Executor: rocketwork.ExecutorIdentity{Kind: "inn", Value: "000000000000", ExpectedID: 7, EmploymentKind: "selfemployed"}, Terms: rocketwork.TaskTerms{ExternalID: "integration-check:" + id, ExecutorINN: "000000000000", EmploymentKind: "selfemployed", AmountKopecks: 10000, Title: "Synthetic", Description: "Synthetic service", SignatureForm: "online", DocumentName: "Synthetic agreement", PeriodFrom: "2026-11-01", PeriodTo: "2026-11-30", ActRequired: true}, MaxCommission: 100, Actions: []string{"create", "complete", "sign_act", "pay"}}
	m.TermsHash = rwDigest(m.Terms)
	return m
}
func writeRWFixture(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRWCheckManifestRejectsChangesBeforeNetwork(t *testing.T) {
	m := rwManifestFixture("http://127.0.0.1:1")
	o := integrationCheckOptions{system: "rocketwork", scenario: "execute", target: m.Target, manifest: filepath.Join(t.TempDir(), "manifest.json"), runDir: t.TempDir(), execute: true, approvedHash: rwDigest(m)}
	c := Config{Env: EnvDevelopment, RocketWork: RocketWorkConfig{Mode: RocketWorkLive, Target: m.Target, BaseURL: m.Origin, AccountKey: m.AccountKey, Token: "synthetic"}}
	writeRWFixture(t, o.manifest, m)
	if _, _, err := loadRWCheckManifest(c, o); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*rwCheckManifest){func(m *rwCheckManifest) { m.Target = "sandbox" }, func(m *rwCheckManifest) { m.AccountKey = "other" }, func(m *rwCheckManifest) { m.Executor.ExpectedID++ }, func(m *rwCheckManifest) { m.Terms.AmountKopecks++ }, func(m *rwCheckManifest) { m.TaskID = 77 }, func(m *rwCheckManifest) { m.Terms.Autopayable = true }} {
		changed := m
		change(&changed)
		writeRWFixture(t, o.manifest, changed)
		if _, _, err := loadRWCheckManifest(c, o); err == nil {
			t.Fatal("changed approval accepted")
		}
	}
	for _, target := range []string{"sandbox", "local-emulator"} {
		if rwOrigin(target, rocketwork.DefaultBaseURL) {
			t.Fatal("production mislabeled as test")
		}
	}
	writeRWFixture(t, o.manifest, m)
	s, unlock, err := openRWCheckState(o.runDir, rwDigest(m), m)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRWCheckState(o.runDir, rwDigest(m), m); err == nil {
		t.Fatal("parallel run unlocked")
	}
	unlock()
	s.ManifestHash = "changed"
	if err := saveRWCheckState(o.runDir, s); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRWCheckState(o.runDir, rwDigest(m), m); err == nil {
		t.Fatal("changed state accepted")
	}
}

func TestRWCheckDocumentedManifest(t *testing.T) {
	var output bytes.Buffer
	p := RunParams{Command: "integration-check", Args: []string{"--system", "rocketwork", "--target", "local-emulator", "--manifest", "../../docs/launch/examples/rocketwork-manifest.json", "--scenario", "preflight"}, Stdout: &output, Stderr: &output}
	if code := Run(context.Background(), p); code != 0 {
		t.Fatalf("documented v2 preflight=%d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), `"network_used":false`) {
		t.Fatal("preflight did not declare offline result")
	}
}

func TestRWCheckHTTPEmulatorResumeNeverRepeatsCreateOrPay(t *testing.T) {
	d := dbtest.New(t)
	em := rocketworktest.New(t)
	server := httptest.NewServer(em)
	defer server.Close()
	m := rwManifestFixture(server.URL)
	o := integrationCheckOptions{system: "rocketwork", scenario: "execute", target: m.Target, manifest: filepath.Join(t.TempDir(), "manifest.json"), runDir: t.TempDir(), execute: true, approvedHash: rwDigest(m)}
	writeRWFixture(t, o.manifest, m)
	c := Config{Env: EnvDevelopment, RocketWork: RocketWorkConfig{Mode: RocketWorkLive, Target: m.Target, BaseURL: m.Origin, AccountKey: m.AccountKey, Token: em.Token, Rate: 10000}}
	in := &infra{pool: d.Pool, clock: clock.System{}, log: logx.Discard()}
	ctx := context.Background()
	o.scenario = "inspect"
	if err := in.executeRWCheck(ctx, c, o); err != nil {
		t.Fatal(err)
	}
	if em.WriteCount("POST", "/api/tasks") != 0 {
		t.Fatal("inspect wrote")
	}
	o.scenario = "execute"
	em.LoseNext("POST", "/api/tasks")
	if err := in.executeRWCheck(ctx, c, o); err == nil {
		t.Fatal("lost response hidden")
	}
	em.HideLookups(1)
	o.scenario = "resume"
	if err := in.executeRWCheck(ctx, c, o); err == nil {
		t.Fatal("absence allowed retry")
	}
	var s rwCheckState
	if err := in.executeRWCheck(ctx, c, o); err != nil {
		t.Fatal(err)
	}
	if err := readRWJSON(filepath.Join(o.runDir, "state.json"), &s); err != nil {
		t.Fatal(err)
	}
	payPath := fmt.Sprintf("/api/tasks/%d/pay", s.TaskID)
	em.LoseNext("PATCH", payPath)
	lost := 0
	for i := 0; i < 4; i++ {
		if err := in.executeRWCheck(ctx, c, o); err != nil {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("expected one uncertain pay, got %d", lost)
	}
	if err := readRWJSON(filepath.Join(o.runDir, "state.json"), &s); err != nil {
		t.Fatal(err)
	}
	if s.Operations["pay"] != "confirmed" || em.WriteCount("POST", "/api/tasks") != 1 || em.WriteCount("PATCH", payPath) != 1 {
		t.Fatal("run did not converge")
	}
	em.SetTask(s.TaskID, map[string]any{"status": "paid", "commission": "0.10", "receipt_uri": "https://receipt.example.test/synthetic"})
	o.scenario = "reconcile"
	o.execute = false
	if err := in.executeRWCheck(ctx, c, o); err != nil {
		t.Fatal(err)
	}
	// A different actual account stops before any task mutation.
	em.ConfigureWithdrawal("111111111111")
	if err := in.executeRWCheck(ctx, c, o); err == nil {
		t.Fatal("foreign provider account accepted")
	}

}
