package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
)

func TestConfiguredRuntimeRoles(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	log := logx.Discard()
	_, err := d.Owner.Exec(ctx, `CREATE ROLE test_rw_policy LOGIN NOINHERIT PASSWORD 'synthetic-policy'; CREATE ROLE test_rw_runner LOGIN NOINHERIT PASSWORD 'synthetic-runner'`)
	if err != nil {
		t.Fatal(err)
	}
	migrateControlTo(t, d, 28)
	if err = db.MigrateURLWithControlRoles(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateUp, log, db.ControlRoles{Policy: "test_rw_policy", Runner: "test_rw_runner"}); err != nil {
		t.Fatal(err)
	}
	connect := func(user, password string) *pgxpool.Pool {
		t.Helper()
		p, err := pgxpool.New(ctx, strings.Replace(d.OwnerURL, "maxim:maxim@", user+":"+password+"@", 1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	policyPool := connect("test_rw_policy", "synthetic-policy")
	runnerPool := connect("test_rw_runner", "synthetic-runner")
	owner := New(policyPool, log)
	runner := New(runnerPool, log)
	if _, err = owner.VerifyRole(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.VerifyRole(ctx, false); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	grant := uuid.NewString()
	p := domain.Policy{ArmingID: "outside-db", RunID: id, ManifestHash: strings.Repeat("a", 64), AccountID: "physical", CompanyINN: "1234567890", Version: 1, FixedAmount: 1000, MaxCommission: 100, TotalBudget: 10000, DailyBudget: 5000, DailyOperations: 3, Executors: []domain.Executor{{ID: 1, INN: "123456789012", Employment: "selfemployed"}}, Actions: []domain.Action{domain.Create}, NotBefore: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
	if err = owner.Configure(ctx, p, "alias"); err != nil {
		t.Fatal(err)
	}
	if err = owner.Issue(ctx, grant, p); err != nil {
		t.Fatal(err)
	}
	if err = runner.Issue(ctx, uuid.NewString(), p); err == nil {
		t.Fatal("runner issued grant")
	}
	r := domain.Run{ID: id, GrantID: grant, ManifestHash: p.ManifestHash, Manifest: json.RawMessage(`{}`), ExternalID: "rw-diagnostic:" + id, Nominal: 1000, FeeCeiling: 100}
	if _, err = runner.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, pool := range []*pgxpool.Pool{d.Pool, runnerPool} {
		for _, sql := range []string{`UPDATE rw_check_account SET killed=false`, `UPDATE rw_check_grant SET revoked_at=NULL`, `UPDATE rw_check_run SET task_id=999`, `UPDATE rw_check_ownership SET task_id=999`, `TRUNCATE rw_check_action`} {
			if _, err = pool.Exec(ctx, sql); err == nil {
				t.Fatalf("role bypass: %s", sql)
			}
		}
	}
	if _, err = runnerPool.Exec(ctx, `INSERT INTO ledger_transaction(id) VALUES(gen_random_uuid())`); err == nil {
		t.Fatal("runner got arbitrary ledger writes")
	}
	if err = owner.Kill(ctx, p.AccountID, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := runner.Intent(ctx, id, domain.Create, strings.Repeat("b", 64)); err != nil || !ok {
		t.Fatalf("runner intent: %v %v", ok, err)
	}
	if err = runner.Freeze(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = runner.Kill(ctx, p.AccountID, false); err == nil {
		t.Fatal("runner rearmed account")
	}
	if err = runner.Reauthorize(ctx, id, grant); err == nil {
		t.Fatal("runner reauthorized its own run")
	}
}
