package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/billing/domain"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/integration/bank/tbank"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func tbankCheckPreflight(c Config, vars map[string]string, o integrationCheckOptions) error {
	real := checkEndpoint(c.TBank.BaseURL, tbank.ProductionURL)
	if c.TBank.Mode != "live" || c.TBank.Token == "" || (!checkEndpoint(c.TBank.BaseURL, tbank.SandboxURL) && !real) {
		return errors.New("tbank checks require live adapter, approved endpoint and read token")
	}
	if real && (!c.TBank.DevRealEnabled || o.target != "production") {
		return errors.New("real bank diagnostics require TBANK_DEV_REAL_ENABLED and explicit --target production")
	}
	if !real && o.target != "" && o.target != "sandbox" {
		return errors.New("bank target does not match endpoint")
	}
	if o.scenario == "sync" && !c.bankAutoSyncEnabled() {
		return errors.New("bank sync requires controlled admission or sandbox automation")
	}
	var bad []string
	c.validateTBank(func(f, _ string) { bad = append(bad, f) })
	if len(bad) > 0 {
		return errors.New("invalid bank configuration: " + strings.Join(bad, ", "))
	}
	if vars["INTEGRATION_CHECK_TBANK_ACCOUNT"] != c.Billing.PlatformBankAccount {
		return errors.New("dedicated bank account allowlist mismatch")
	}
	if vars["INTEGRATION_CHECK_DATABASE_URL"] != c.DatabaseURL || c.DatabaseURL == "" {
		return errors.New("explicit dedicated check database required")
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || !strings.Contains(strings.ToLower(u.Path), "tbank_check") {
		return errors.New("check database name must include tbank_check")
	}
	// This assembly never connects storage, sends mail, or starts other jobs.
	if o.scenario == "sync" && o.runDir == "" {
		return errors.New("bank sync run directory required")
	}
	return nil
}

type bankCheckState struct {
	Version              int
	Scope                string
	Status               string
	At                   time.Time
	Operations, Incoming int
	Complete             bool
}

func (in *infra) executeTBankCheck(ctx context.Context, c Config, o integrationCheckOptions) error {
	source, err := in.bankSource(c, true)
	if err != nil {
		return err
	}
	from := c.bankSyncConfig().Start
	to := from.Add(c.TBank.Period)
	if o.from != "" {
		from, err = time.Parse(time.RFC3339, o.from)
		if err != nil {
			return errors.New("invalid replay from")
		}
		to, err = time.Parse(time.RFC3339, o.to)
		if err != nil || !from.Before(to) || to.After(in.clock.Now()) {
			return errors.New("invalid replay to")
		}
	}
	raw, _ := json.Marshal([]string{c.DatabaseURL, c.Billing.PlatformBankAccount, from.Format(time.RFC3339), to.Format(time.RFC3339)})
	hash := sha256.Sum256(raw)
	scope := hex.EncodeToString(hash[:])
	state := bankCheckState{Version: 1, Scope: scope, Status: "started", At: in.clock.Now()}
	save := func() error { return nil }
	if o.runDir != "" {
		if err := os.MkdirAll(o.runDir, 0700); err != nil {
			return err
		}
		path := filepath.Join(o.runDir, "tbank-state.json")
		if body, err := os.ReadFile(path); err == nil {
			var old bankCheckState
			if json.Unmarshal(body, &old) != nil || old.Scope != scope {
				return errors.New("bank check run scope mismatch")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		save = func() error {
			body, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			tmp := path + ".tmp"
			if err = os.WriteFile(tmp, body, 0600); err != nil {
				return err
			}
			return os.Rename(tmp, path)
		}
		if err := save(); err != nil {
			return err
		}
	}
	if o.scenario == "sync" {
		runner := tx.NewRunner(in.pool, in.log)
		a := audit.New(in.pool, in.clock, in.log)
		accounts := accountpg.New(in.pool, in.log)
		ledger, _ := newLedgerService(c, in, runner, a)
		notifier, _ := newNotificationService(c, in, runner, job.NewQueue(in.pool, in.clock, in.log), accounts, channelpg.New(in.pool, in.log))
		billing, _ := newBillingService(c, in, runner, a, accounts, ledger, notifier)
		billing.BankSource = source
		if o.from != "" {
			if err := billing.PrepareBankReplay(ctx, from, to); err != nil {
				return err
			}
		}
		res, err := billing.SyncBank(ctx)
		if err != nil {
			state.Status = "interrupted"
			_ = save()
			return err
		}
		state.Incoming = res.Incoming
		state.Status = "sync_complete"
		state.Complete = true
		in.log.InfoContext(ctx, "bank sandbox sync finished", "incoming", res.Incoming, "matched", res.Matched, "duplicates", res.Duplicates, "conflicts", res.Conflicts, "full_contract_verified", false)
		return save()
	}
	return in.readTBankCheck(ctx, source, c, from, to, &state, save)
}

func (in *infra) readTBankCheck(ctx context.Context, source domain.BankSource, c Config, from, to time.Time, state *bankCheckState, save func() error) error {
	cursor := ""
	seen := map[string]bool{}
	for n := 0; n < 1000; n++ {
		p, err := source.ReadPage(ctx, domain.BankQuery{Account: c.Billing.PlatformBankAccount, From: from, To: to, Cursor: cursor})
		if err != nil {
			state.Status = "interrupted"
			_ = save()
			return err
		}
		state.Operations += p.Operations
		state.Incoming += len(p.Incoming)
		if p.NextCursor == "" {
			state.Status = "read_complete"
			state.Complete = true
			in.log.InfoContext(ctx, "bank sandbox read completed", "operations", state.Operations, "incoming", state.Incoming, "full_contract_verified", false)
			return save()
		}
		if seen[p.NextCursor] || p.NextCursor == cursor {
			state.Status = "cursor_cycle"
			if err := save(); err != nil {
				return err
			}
			in.log.WarnContext(ctx, "bank statement pagination did not complete", "reason", "repeated_cursor", "operations_read", state.Operations, "full_contract_verified", false)
			return errors.New("bank repeated cursor")
		}
		seen[p.NextCursor] = true
		cursor = p.NextCursor
	}
	state.Status = "page_limit"
	if err := save(); err != nil {
		return err
	}
	return errors.New("bank read page limit exceeded")
}
