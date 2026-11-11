package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/dbtest"
)

// Exercise the deploy SQL against a fully migrated DB: this is the case in
// which rerunning goose up cannot supply grants skipped by migration 00029.
func TestProvisionGrantsAfterMigrations(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	if _, err := d.Owner.Exec(ctx, `CREATE ROLE maxim_rw_runner LOGIN NOINHERIT; CREATE ROLE maxim_rw_policy LOGIN NOINHERIT`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../../deploy/postgres/rocketwork-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	var sql strings.Builder
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), `\`) {
			sql.WriteString(line + "\n")
		}
	}
	query := strings.ReplaceAll(sql.String(), ":'app_role'", "'"+dbtest.AppRole+"'")
	if _, err = d.Owner.Exec(ctx, `INSERT INTO rw_check_audit(actor,operation) VALUES('setup-test','retained-history')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = d.Owner.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	var retained int
	if err = d.Owner.QueryRow(ctx, `SELECT count(*) FROM rw_check_audit WHERE actor='setup-test' AND operation='retained-history'`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("history lost: %d %v", retained, err)
	}
	var runnerIssue, runnerPrepare, runnerPaidAt, policyReauthorize, appIssue, runnerLedger bool
	err = d.Owner.QueryRow(ctx, `SELECT
	 has_function_privilege('maxim_rw_runner','rw_check_issue(uuid,jsonb)','EXECUTE'),
	 has_function_privilege('maxim_rw_runner','rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint)','EXECUTE'),
	 has_function_privilege('maxim_rw_runner','rw_check_paid_at(uuid,timestamptz)','EXECUTE'),
	 has_function_privilege('maxim_rw_policy','rw_check_reauthorize(uuid,uuid)','EXECUTE'),
	 has_function_privilege('maxim_app','rw_check_issue(uuid,jsonb)','EXECUTE'),
	 has_table_privilege('maxim_rw_runner','ledger_transaction','INSERT')`).Scan(&runnerIssue, &runnerPrepare, &runnerPaidAt, &policyReauthorize, &appIssue, &runnerLedger)
	if err != nil {
		t.Fatal(err)
	}
	if runnerIssue || !runnerPrepare || !runnerPaidAt || !policyReauthorize || appIssue || runnerLedger {
		t.Fatalf("unexpected authority: issue=%t prepare=%t paidAt=%t reauthorize=%t appIssue=%t ledger=%t", runnerIssue, runnerPrepare, runnerPaidAt, policyReauthorize, appIssue, runnerLedger)
	}
	if _, err = d.Owner.Exec(ctx, `GRANT maxim_rw_runner TO maxim_app`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Owner.Exec(ctx, query); err == nil {
		t.Fatal("unsafe role membership accepted")
	}
	// The rejected explicit transaction leaves the connection in a transaction.
	_, _ = d.Owner.Exec(ctx, "ROLLBACK")
	_, _ = d.Owner.Exec(ctx, `REVOKE maxim_rw_runner FROM maxim_app`)
}
