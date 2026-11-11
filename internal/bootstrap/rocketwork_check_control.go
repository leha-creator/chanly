package bootstrap

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"

	"github.com/caarlos0/env/v11"
	"github.com/leha-creator/chanly/internal/bootstrap/bridge"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/tx"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
	checkdomain "github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	checkpg "github.com/leha-creator/chanly/internal/rocketworkcheck/postgres"
)

// This command intentionally bypasses ordinary app bootstrap. It cannot start
// workers, seed, HTTP handlers, onboarding or production automation.
func runRocketWorkControl(ctx context.Context, p RunParams) int {
	if len(p.Args) == 0 {
		return 2
	}
	command := p.Args[0]
	f := flag.NewFlagSet("rocketwork-check", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	manifestPath := f.String("manifest", "", "")
	policyPath := f.String("policy", "", "")
	runID := f.String("run", "", "")
	grantID := f.String("grant", "", "")
	account := f.String("account", "", "")
	alias := f.String("alias", "", "")
	if f.Parse(p.Args[1:]) != nil || f.NArg() != 0 {
		return 2
	}
	owner := false
	switch command {
	case "configure-account", "issue", "revoke", "kill", "arm", "reauthorize", "approve-ui":
		owner = true
	case "fingerprint", "prepare", "inspect", "execute", "resume", "reconcile", "status", "import", "serve":
	default:
		return 2
	}
	vars := env.ToMap(p.Environ)
	level, validLevel := logx.ParseLevel(vars["LOG_LEVEL"], slog.LevelInfo)
	if !validLevel {
		return 2
	}
	log := logx.New(p.Stdout, level, "rocketwork-check")
	writeAttempted := false
	fail := func() int {
		log.WarnContext(ctx, "diagnostic command denied", "command", command, "write_attempted", writeAttempted, "live_verified", false)
		return 1
	}
	var m checkdomain.Manifest
	if *manifestPath != "" && command != "import" {
		if readRWJSON(*manifestPath, &m) != nil {
			return fail()
		}
	}
	if command == "fingerprint" {
		if m.Version != 2 || m.Purpose != checkdomain.Diagnostic || m.RunID == "" || m.GrantID == "" || !rwOrigin(m.Target, m.Origin) || checkTermsForManifest(m) != nil {
			return fail()
		}
		_ = json.NewEncoder(p.Stdout).Encode(map[string]any{"manifest_sha256": checkdomain.Digest(m), "network_used": false, "live_verified": false})
		return 0
	}
	dsn := vars["ROCKETWORK_CONTROL_DATABASE_URL"]
	if owner {
		dsn = vars["ROCKETWORK_POLICY_DATABASE_URL"]
	}
	if dsn == "" {
		return fail()
	}
	pool, err := db.Open(ctx, dsn, 4, log)
	if err != nil {
		return fail()
	}
	defer pool.Close()
	store := checkpg.New(pool, log)
	actor, err := store.VerifyRole(ctx, owner)
	if err != nil {
		return fail()
	}
	log.InfoContext(ctx, "diagnostic operator authenticated", "actor", actor, "policy_owner", owner)
	if owner {
		var policy checkdomain.Policy
		switch command {
		case "approve-ui":
			var run checkdomain.Run
			run, err = approveRWUI(ctx, store, m, *policyPath, vars["ROCKETWORK_CHECK_ARMING_ID"])
			if err == nil {
				err = json.NewEncoder(p.Stdout).Encode(map[string]any{"run_id": run.ID, "write_attempted": false, "next_step": "operator verifies account arming, then configures this run for UI"})
			}
		case "configure-account", "issue":
			if readRWJSON(*policyPath, &policy) != nil || policy.Validate() != nil {
				return fail()
			}
			if command == "issue" {
				if vars["ROCKETWORK_CHECK_ARMING_ID"] == "" || policy.ArmingID != vars["ROCKETWORK_CHECK_ARMING_ID"] {
					return fail()
				}
				err = store.Issue(ctx, *grantID, policy)
			} else {
				err = store.Configure(ctx, policy, *alias)
			}
		case "revoke":
			err = store.Revoke(ctx, *grantID)
		case "reauthorize":
			err = store.Reauthorize(ctx, *runID, *grantID)
		case "kill", "arm":
			err = store.Kill(ctx, *account, command == "kill")
		}
		if err != nil {
			return fail()
		}
		return 0
	}
	if command == "status" {
		status, err := store.Status(ctx, *runID)
		if err != nil {
			return fail()
		}
		if json.NewEncoder(p.Stdout).Encode(status) != nil {
			return fail()
		}
		return 0
	}
	origin := vars["ROCKETWORK_BASE_URL"]
	if origin == "" {
		origin = rocketwork.DefaultBaseURL
	}
	target := vars["ROCKETWORK_TARGET"]
	if !rwOrigin(target, origin) || (command != "prepare" && (target != "production" || vars["ROCKETWORK_TOKEN"] == "")) {
		return fail()
	}
	cl := clock.System{}
	factory := func(auth rocketwork.WriteAuthorizer) *rocketwork.Client {
		if auth != nil && command != "serve" {
			auth = trackedRWAuthorization{delegate: auth, allowed: func() { writeAttempted = true }}
		}
		out := outbound.New(pool, cl, log, outbound.Options{System: outbound.SystemRocketWork, HTTP: rocketwork.NewHTTPClient(), NoRetry: true})
		limiter := ratelimit.New(pool, cl, log, ratelimit.Options{})
		return rocketwork.New(out, limiter, rocketwork.Options{BaseURL: origin, Token: vars["ROCKETWORK_TOKEN"], Rate: 5, Authorizer: auth}, log)
	}
	if command == "import" {
		legacyCfg := Config{RocketWork: RocketWorkConfig{Mode: RocketWorkLive, Target: target, BaseURL: origin, Token: vars["ROCKETWORK_TOKEN"], AccountKey: *alias, Rate: 5}}
		legacy, hash, err := loadRWCheckManifest(legacyCfg, integrationCheckOptions{manifest: *manifestPath, target: target, scenario: "inspect"})
		if err != nil {
			return fail()
		}
		client := factory(nil)
		providerAccount, err := client.Account(ctx)
		if err != nil || providerAccount.CompanyINN != legacy.CompanyINN {
			return fail()
		}
		var task rocketwork.TaskSnapshot
		found := false
		if legacy.TaskID > 0 {
			task, err = client.GetTask(ctx, legacy.TaskID)
			found = err == nil
		} else {
			task, found, err = client.FindTask(ctx, legacy.Terms.ExternalID)
		}
		if err != nil || !found || !rwTaskMatches(task, legacy) {
			return fail()
		}
		canonical, err := store.AccountForAlias(ctx, *alias)
		if err != nil {
			return fail()
		}
		termsHash := checkdomain.Digest(checkdomain.Terms(legacy.Terms))
		if store.ImportLegacy(ctx, legacy.RunID, canonical, legacy.CompanyINN, legacy.Terms.ExternalID, task.ID, hash, termsHash, task.Status) != nil {
			return fail()
		}
		_ = json.NewEncoder(p.Stdout).Encode(map[string]any{"legacy_import_id": legacy.RunID, "task_id": task.ID, "imported_terms_hash": termsHash, "write_attempted": false, "next_step": "review v2 manifest and issue a separate grant; do not recreate task"})
		return 0
	}
	s := &checkapp.Service{Store: store, Log: log, Tx: tx.NewRunner(pool, log), ArmingID: vars["ROCKETWORK_CHECK_ARMING_ID"], Provider: bridge.CheckProvider{Factory: factory, Origin: origin, Target: target}}
	s.Ledger = bridge.CheckLedger{Ledger: ledgerapp.New(ledgerapp.Deps{Store: ledgerpg.New(pool, log), Log: log, Clock: cl})}
	if command == "serve" {
		return serveRWCheck(ctx, vars, *runID, s, store, log)
	}
	switch command {
	case "prepare":
		_, err = s.Prepare(ctx, m)
	case "execute", "resume":
		err = s.Advance(ctx, *runID, true)
	case "reconcile", "inspect":
		err = s.Advance(ctx, *runID, false)
	}
	if err != nil {
		return fail()
	}
	log.InfoContext(ctx, "diagnostic command completed", "command", command, "run", *runID, "write_attempted", writeAttempted, "live_verified", false)
	return 0
}

func checkTermsForManifest(m checkdomain.Manifest) error {
	external := "rw-diagnostic:" + m.RunID
	if m.LegacyImportID != "" {
		external = "integration-check:" + m.LegacyImportID
	}
	if m.Terms.AmountKopecks <= 0 || m.Terms.Autopayable || m.Terms.Autocomplete || m.Terms.Autosignable || m.Terms.ExternalID != external {
		return checkdomain.ErrDenied
	}
	return rocketwork.TaskTerms(m.Terms).Validate()
}

type trackedRWAuthorization struct {
	delegate rocketwork.WriteAuthorizer
	allowed  func()
}

func (a trackedRWAuthorization) AuthorizeWrite(ctx context.Context, w rocketwork.WriteRequest) error {
	if err := a.delegate.AuthorizeWrite(ctx, w); err != nil {
		return err
	}
	a.allowed()
	return nil
}
