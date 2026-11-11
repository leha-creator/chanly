package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/integration/rocketwork/rocketworktest"
	ledgerapp "github.com/leha-creator/chanly/internal/ledger/app"
	ledgerpg "github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
	"github.com/leha-creator/chanly/internal/platform/tx"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	checkpg "github.com/leha-creator/chanly/internal/rocketworkcheck/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestDiagnosticDurableWorkflow(t *testing.T) {
	for _, scenario := range []string{"normal", "lost_response", "legacy_import", "fee_overrun", "nominal_mismatch", "cancel", "rejected_create"} {
		t.Run(scenario, func(t *testing.T) {
			lost := scenario == "lost_response"
			d := dbtest.New(t)
			ctx := context.Background()
			log := logx.Discard()
			cl := clock.System{}
			emulator := rocketworktest.New(t)
			server := httptest.NewServer(emulator)
			defer server.Close()
			factory := func(a rocketwork.WriteAuthorizer) *rocketwork.Client {
				out := outbound.New(d.Owner, cl, log, outbound.Options{System: outbound.SystemRocketWork, HTTP: server.Client(), NoRetry: true})
				return rocketwork.New(out, ratelimit.New(d.Owner, cl, log, ratelimit.Options{}), rocketwork.Options{BaseURL: server.URL, Token: emulator.Token, Rate: 10000, Authorizer: a}, log)
			}
			store := checkpg.New(d.Owner, log)
			m := domain.Manifest{Version: 2, RunID: uuid.NewString(), GrantID: uuid.NewString(), Purpose: domain.Diagnostic, Target: "local-emulator", Origin: server.URL, AccountID: "physical", AccountKey: "alias", CompanyINN: "000000000000", Executor: domain.Executor{ID: 7, INN: "000000000000", Employment: "selfemployed"}, Actions: []domain.Action{domain.Create, domain.Complete, domain.SignAct, domain.Pay}}
			m.Terms = domain.Terms{ExternalID: "rw-diagnostic:" + m.RunID, ExecutorINN: m.Executor.INN, EmploymentKind: m.Executor.Employment, AmountKopecks: 1000, Title: "Synthetic test", Description: "Synthetic test service", SignatureForm: "online", DocumentName: "Synthetic agreement", PeriodFrom: "2026-11-01", PeriodTo: "2026-11-30", ActRequired: true}
			if scenario == "legacy_import" {
				m.LegacyImportID = m.RunID
				m.Terms.ExternalID = "integration-check:" + m.RunID
				m.ImportedTermsHash = domain.Digest(m.Terms)
				m.Actions = []domain.Action{domain.Complete, domain.SignAct, domain.Pay}
			}
			if scenario == "cancel" {
				m.Actions = []domain.Action{domain.Create, domain.Cancel}
			}
			p := domain.Policy{ArmingID: "external-arming", RunID: m.RunID, ManifestHash: domain.Digest(m), AccountID: m.AccountID, CompanyINN: m.CompanyINN, Version: 1, FixedAmount: 1000, MaxCommission: 100, TotalBudget: 10000, DailyBudget: 5000, DailyOperations: 3, Executors: []domain.Executor{m.Executor}, Actions: m.Actions, NotBefore: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
			if err := store.Configure(ctx, p, "alias"); err != nil {
				t.Fatal(err)
			}
			if scenario == "legacy_import" {
				a, _ := rocketwork.LocalEmulatorAuthorization(server.URL)
				legacy, err := factory(a).CreateTask(ctx, rocketwork.TaskTerms(m.Terms))
				if err != nil {
					t.Fatal(err)
				}
				if err = store.ImportLegacy(ctx, m.RunID, m.AccountID, m.CompanyINN, m.Terms.ExternalID, legacy.ID, strings.Repeat("a", 64), m.ImportedTermsHash, legacy.Status); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Issue(ctx, m.GrantID, p); err != nil {
				t.Fatal(err)
			}
			if err := store.Kill(ctx, p.AccountID, false); err != nil {
				t.Fatal(err)
			}
			service := &checkapp.Service{Store: store, Log: log, ArmingID: p.ArmingID, Provider: CheckProvider{Factory: factory, Origin: server.URL, Target: m.Target}}
			if _, err := service.Prepare(ctx, m); err != nil {
				t.Fatal(err)
			}
			switched := *service
			switched.Provider = CheckProvider{Origin: rocketwork.DefaultBaseURL, Target: "production", Factory: func(rocketwork.WriteAuthorizer) *rocketwork.Client {
				t.Fatal("origin drift reached network composition")
				return nil
			}}
			if err := switched.Advance(ctx, m.RunID, true); err == nil {
				t.Fatal("emulator run promoted to production by runtime env")
			}
			bad := m
			bad.Terms.AmountKopecks++
			if _, err := service.Prepare(ctx, bad); err == nil {
				t.Fatal("amount substitution accepted")
			}
			for name, change := range map[string]func(*domain.Manifest){
				"one kopeck less": func(m *domain.Manifest) { m.Terms.AmountKopecks-- },
				"executor":        func(m *domain.Manifest) { m.Executor.ID++ },
				"alias":           func(m *domain.Manifest) { m.AccountKey = "forged-alias" },
				"autopay":         func(m *domain.Manifest) { m.Terms.Autopayable = true },
				"autosign":        func(m *domain.Manifest) { m.Terms.Autosignable = true },
				"autocomplete":    func(m *domain.Manifest) { m.Terms.Autocomplete = true },
				"withdraw":        func(m *domain.Manifest) { m.Actions = []domain.Action{"withdraw"} },
				"foreign grant":   func(m *domain.Manifest) { m.GrantID = uuid.NewString() },
			} {
				bad := m
				change(&bad)
				if _, err := service.Prepare(ctx, bad); err == nil {
					t.Fatalf("accepted %s", name)
				}
			}
			if scenario != "legacy_import" {
				for _, arming := range []string{"", "restored-old-arming"} {
					other := *service
					other.ArmingID = arming
					if err := other.Advance(ctx, m.RunID, true); err == nil {
						t.Fatal("invalid external arming accepted")
					}
				}
				unavailable := *service
				unavailable.Store = unavailableControlStore{ControlStore: store}
				if err := unavailable.Advance(ctx, m.RunID, true); err == nil {
					t.Fatal("unavailable journal accepted")
				}
				if _, err := factory(nil).CreateTask(ctx, rocketwork.TaskTerms(m.Terms)); err == nil {
					t.Fatal("direct readonly client wrote")
				}
				if emulator.WriteCount("POST", "/api/tasks") != 0 {
					t.Fatal("negative case sent create")
				}
				// The owner simulates an expired DB timestamp without weakening runtime roles.
				if _, err := d.Owner.Exec(ctx, `ALTER TABLE rw_check_grant DISABLE TRIGGER rw_check_grant_guard; UPDATE rw_check_grant SET expires_at=clock_timestamp()-interval '1 second'; ALTER TABLE rw_check_grant ENABLE TRIGGER rw_check_grant_guard`); err != nil {
					t.Fatal(err)
				}
				if err := service.Advance(ctx, m.RunID, true); err == nil {
					t.Fatal("expired grant authorized")
				}
				if _, err := d.Owner.Exec(ctx, `ALTER TABLE rw_check_grant DISABLE TRIGGER rw_check_grant_guard; UPDATE rw_check_grant SET expires_at=clock_timestamp()+interval '1 hour'; ALTER TABLE rw_check_grant ENABLE TRIGGER rw_check_grant_guard`); err != nil {
					t.Fatal(err)
				}
			}
			if lost {
				emulator.LoseNext("POST", "/api/tasks")
			}
			if scenario == "rejected_create" {
				emulator.FailNext("POST", "/api/tasks", 422)
			}
			err := service.Advance(ctx, m.RunID, true)
			if scenario == "rejected_create" {
				if err == nil {
					t.Fatal("rejection ignored")
				}
				r, _ := store.Run(ctx, m.RunID)
				if r.State != "released" {
					t.Fatalf("rejected create retained reserve: %s", r.State)
				}
				return
			}
			if !lost && err != nil {
				t.Fatal(err)
			}
			// Callback before task_id binding uses verified GET + central ownership,
			// with the ordinary application's minimal view privileges.
			located, found, err := factory(nil).FindTask(ctx, m.Terms.ExternalID)
			if err != nil || !found {
				t.Fatal("task lookup failed")
			}
			router := CheckEventRouter{Events: checkapp.Events{Registry: checkpg.New(d.Pool, log), Log: log}, AccountAlias: m.AccountKey, API: factory(nil)}
			if handled, err := router.RouteDiagnostic(ctx, jsonNumber(located.ID)); err != nil || !handled {
				t.Fatalf("early diagnostic event: %v %v", handled, err)
			}
			emulator.SetTask(located.ID, map[string]any{"external_id": "business-owned"})
			if handled, err := router.RouteDiagnostic(ctx, jsonNumber(located.ID)); err != nil || handled {
				t.Fatalf("business event captured: %v %v", handled, err)
			}
			emulator.SetTask(located.ID, map[string]any{"external_id": m.Terms.ExternalID})
			for range 4 {
				_ = service.Advance(ctx, m.RunID, false)
			}
			r, err := store.Run(ctx, m.RunID)
			if err != nil || r.TaskID == nil {
				t.Fatalf("binding missing: %+v %v", r, err)
			}
			if emulator.WriteCount("POST", "/api/tasks") != 1 {
				t.Fatal("create repeated")
			}
			wire := emulator.Task(*r.TaskID)
			if wire["amount_gross"] != "10.00" {
				b, _ := json.Marshal(wire)
				t.Fatalf("wrong amount: %s", b)
			}
			// Recreated service reads exactly the same checkpoint, without local files.
			other := *service
			if scenario == "nominal_mismatch" {
				emulator.SetTask(*r.TaskID, map[string]any{"amount_gross": "10.01"})
				if err = service.Advance(ctx, m.RunID, false); err == nil {
					t.Fatal("wrong principal accepted")
				}
				status, err := store.Status(ctx, m.RunID)
				if err != nil || status["killed"] != true {
					t.Fatalf("account not frozen: %v %v", status, err)
				}
				return
			}
			if scenario == "cancel" {
				emulator.SetTask(*r.TaskID, map[string]any{"signed_at": nil, "executor_signed_at": nil, "commission": "0.00"})
				if err = service.Advance(ctx, m.RunID, true); err != nil {
					t.Fatal(err)
				}
				if err = service.Advance(ctx, m.RunID, false); err != nil {
					t.Fatal(err)
				}
				r, err = store.Run(ctx, m.RunID)
				if err != nil || r.State != "released" {
					t.Fatalf("proven cancellation did not release: %+v %v", r, err)
				}
				return
			}
			if !lost {
				for range 3 {
					if err = service.Advance(ctx, m.RunID, true); err != nil {
						t.Fatal(err)
					}
					if err = service.Advance(ctx, m.RunID, false); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "normal" {
					if _, err = d.Owner.Exec(ctx, `ALTER TABLE rw_check_run DISABLE TRIGGER rw_check_run_guard; UPDATE rw_check_run SET reserved_at=clock_timestamp()-interval '1 day'; ALTER TABLE rw_check_run ENABLE TRIGGER rw_check_run_guard`); err != nil {
						t.Fatal(err)
					}
				}
				emulator.SetTask(*r.TaskID, map[string]any{"status": "paid"})
				if err = service.Advance(ctx, m.RunID, false); err != nil {
					t.Fatal(err)
				}
				pending, _ := store.Run(ctx, m.RunID)
				if pending.State == "settled" {
					t.Fatal("settled without fee evidence")
				}
				fee := int64(50)
				feeText := "0.50"
				if scenario == "fee_overrun" {
					fee = 200
					feeText = "2.00"
				}
				dbTime, err := store.Now(ctx)
				if err != nil {
					t.Fatal(err)
				}
				providerPaidAt := dbTime.Add(-25 * time.Hour)
				emulator.SetTask(*r.TaskID, map[string]any{"status": "paid", "commission": feeText, "executor_amount_gross": "10.00", "paid_at": providerPaidAt.Format(time.RFC3339Nano)})
				service.Tx = tx.NewRunner(d.Owner, log)
				service.Ledger = CheckLedger{Ledger: ledgerapp.New(ledgerapp.Deps{Store: ledgerpg.New(d.Owner, log), Log: log, Clock: cl})}
				// Failure after ledger posting must roll back both expense and settlement.
				service.Store = failSettlementStore{ControlStore: store}
				if err = service.Advance(ctx, m.RunID, false); err == nil {
					t.Fatal("injected settlement error ignored")
				}
				var count int
				if err = d.Owner.QueryRow(ctx, `SELECT count(*) FROM ledger_transaction WHERE kind='rw_diagnostic'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial ledger commit: %d %v", count, err)
				}
				service.Store = store
				for range 2 {
					err = service.Advance(ctx, m.RunID, false)
					if scenario == "fee_overrun" {
						if !errors.Is(err, domain.ErrFinancialMismatch) {
							t.Fatalf("fee overrun not surfaced after accounting: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "normal" {
					// Yesterday's hold settles as today's financial spend. A new grant
					// cannot exploit the old reservation date to spend the daily ceiling twice.
					if err = store.Kill(ctx, m.AccountID, false); err != nil {
						t.Fatal(err)
					} // Owner admits writes after the proven accounting recovery.
					next := m
					next.RunID = uuid.NewString()
					next.GrantID = uuid.NewString()
					next.Terms.ExternalID = "rw-diagnostic:" + next.RunID
					p2 := p
					p2.RunID = next.RunID
					p2.ManifestHash = domain.Digest(next)
					p2.DailyBudget = 2100
					if err = store.Configure(ctx, p2, "alias"); err != nil {
						t.Fatal(err)
					}
					if err = store.Issue(ctx, next.GrantID, p2); err != nil {
						t.Fatal(err)
					}
					if _, err = service.Prepare(ctx, next); err != nil {
						t.Fatal(err)
					}
					if err = service.Advance(ctx, next.RunID, true); err == nil {
						t.Fatal("yesterday's reserve bypassed today's settled spend")
					}
					if emulator.WriteCount("POST", "/api/tasks") != 1 {
						t.Fatal("daily limit bypass sent another create")
					}
					status, err := store.Status(ctx, next.RunID)
					if err != nil || status["daily_spent_kopecks"] != "1050" || status["held_kopecks"] != "0" {
						t.Fatalf("rollover status: %v %v", status, err)
					}
				}
				r, err = store.Run(ctx, m.RunID)
				if err != nil || r.State != "settled" || r.ActualFee == nil || int64(*r.ActualFee) != fee {
					t.Fatalf("settlement %+v %v", r, err)
				}
				if r.ProviderPaidAt == nil || !r.ProviderPaidAt.Equal(providerPaidAt) || r.SettledAt == nil || !r.SettledAt.After(providerPaidAt) {
					t.Fatal("provider time replaced trusted control settlement date")
				}
				if err = d.Owner.QueryRow(ctx, `SELECT count(*) FROM ledger_transaction WHERE kind='rw_diagnostic'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("duplicate ledger: %d %v", count, err)
				}
				var balance int64
				if err = d.Owner.QueryRow(ctx, `SELECT balance FROM ledger_account WHERE kind='rocketwork_deposit'`).Scan(&balance); err != nil || balance != -1000-fee {
					t.Fatalf("deposit=%d %v", balance, err)
				}
				ledgerStore := ledgerpg.New(d.Owner, log)
				assets, liabilities, err := ledgerStore.SidesTotal(ctx)
				if err != nil || assets != liabilities {
					t.Fatalf("diagnostic expense broke ledger sides: %d %d %v", assets, liabilities, err)
				}
				mismatches, err := ledgerStore.CacheMismatches(ctx)
				if err != nil || len(mismatches) != 0 {
					t.Fatalf("diagnostic expense broke ledger cache: %+v %v", mismatches, err)
				}
				if scenario == "fee_overrun" {
					status, _ := store.Status(ctx, m.RunID)
					if status["killed"] != true {
						t.Fatal("fee overrun did not freeze account")
					}
				}
				return
			}
			if err = store.Revoke(ctx, m.GrantID); err != nil {
				t.Fatal(err)
			}
			if err = other.Advance(ctx, m.RunID, true); err == nil {
				t.Fatal("revoked grant permitted complete")
			}
			if emulator.WriteCount("PATCH", "/api/tasks/"+jsonNumber(*r.TaskID)+"/complete") != 0 {
				t.Fatal("write after revoke")
			}
			if err = other.Advance(ctx, m.RunID, false); err != nil {
				t.Fatal(err)
			}
			// An independently issued recovery admission changes authority only.
			// The same task, hold and committed create intent survive the rebind.
			previous, err := store.Run(ctx, m.RunID)
			if err != nil {
				t.Fatal(err)
			}
			newGrant := uuid.NewString()
			p.ArmingID = "new-external-arming"
			p.Version++
			if err = store.Issue(ctx, newGrant, p); err != nil {
				t.Fatal(err)
			}
			if err = store.Reauthorize(ctx, m.RunID, newGrant); err != nil {
				t.Fatal(err)
			}
			if err = other.Advance(ctx, m.RunID, true); err == nil {
				t.Fatal("old arming survived reauthorization")
			}
			other.ArmingID = p.ArmingID
			if err = other.Advance(ctx, m.RunID, true); err != nil {
				t.Fatal(err)
			}
			after, err := store.Run(ctx, m.RunID)
			if err != nil || after.TaskID == nil || *after.TaskID != *previous.TaskID || after.GrantID != newGrant || after.ReservedAt == nil || !after.ReservedAt.Equal(*previous.ReservedAt) {
				t.Fatalf("recovery changed immutable operation: %+v %v", after, err)
			}
			if emulator.WriteCount("POST", "/api/tasks") != 1 {
				t.Fatal("recovery repeated create")
			}
		})
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

type failSettlementStore struct{ domain.ControlStore }

type unavailableControlStore struct{ domain.ControlStore }

func (unavailableControlStore) Run(context.Context, string) (domain.Run, error) {
	return domain.Run{}, errors.New("injected control DB outage")
}

func (f failSettlementStore) Settle(context.Context, string, money.Kopecks, money.Kopecks, string) error {
	return errors.New("injected commit failure")
}
