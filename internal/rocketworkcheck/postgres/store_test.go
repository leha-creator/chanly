package postgres

import (
	"context"
	"encoding/json"
	"os"
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

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestControlJournalPermissionsAndImmutableRun(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	// Dedicated execution role is deliberately not a member of the app role.
	_, err := d.Owner.Exec(ctx, `CREATE ROLE check_runner LOGIN PASSWORD 'test-only'; GRANT CONNECT ON DATABASE maxim TO check_runner; GRANT USAGE ON SCHEMA public TO check_runner; GRANT SELECT ON rw_check_account,rw_check_grant,rw_check_run,rw_check_action TO check_runner; GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) TO check_runner`)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := pgxpool.New(ctx, strings.Replace(d.OwnerURL, "maxim:maxim@", "check_runner:test-only@", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	g := uuid.NewString()
	id := uuid.NewString()
	p := domain.Policy{AccountID: "physical", CompanyINN: "1234567890", Version: 1, FixedAmount: 1000, MaxCommission: 100, TotalBudget: 10000, DailyBudget: 5000, DailyOperations: 3, Executors: []domain.Executor{{ID: 1, INN: "123456789012", Employment: "selfemployed"}}, Actions: []domain.Action{domain.Create}, NotBefore: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
	b, _ := json.Marshal(p)
	p.RunID = id
	p.ManifestHash = strings.Repeat("a", 64)
	b, _ = json.Marshal(p)
	_, err = d.Owner.Exec(ctx, `INSERT INTO rw_check_account(id,company_inn,total_limit,daily_limit,daily_operations) VALUES('physical','1234567890',10000,5000,3)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Owner.Exec(ctx, `INSERT INTO rw_check_grant(id,account_id,version,purpose,issued_by,policy,not_before,expires_at) VALUES($1,'physical',1,'diagnostic','owner',$2,now()-interval '1 hour',now()+interval '1 hour')`, g, b)
	if err != nil {
		t.Fatal(err)
	}
	s := New(runner, logx.Discard())
	if actor, err := s.VerifyRole(ctx, false); err != nil || actor != "check_runner" {
		t.Fatalf("runner identity %s %v", actor, err)
	}
	if _, err := s.VerifyRole(ctx, true); err == nil {
		t.Fatal("runner became policy owner")
	}
	if err := s.Issue(ctx, uuid.NewString(), p); err == nil {
		t.Fatal("runner issued grant")
	}
	r := domain.Run{ID: id, GrantID: g, ManifestHash: strings.Repeat("a", 64), Manifest: []byte(`{"approved":true}`), ExternalID: "rw-diagnostic:" + id, Nominal: 1000, FeeCeiling: 100}
	if _, err = s.Prepare(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Prepare(ctx, r); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	r.Manifest = []byte(`{"approved":false}`)
	if _, err = s.Prepare(ctx, r); err == nil {
		t.Fatal("changed payload accepted")
	}
	for _, pool := range []*pgxpool.Pool{d.Pool, runner} {
		for _, sql := range []string{`UPDATE rw_check_account SET total_limit=999999`, `DELETE FROM rw_check_grant`, `TRUNCATE rw_check_action`, `UPDATE rw_check_run SET state='prepared'`, `INSERT INTO rw_check_grant(id) VALUES(gen_random_uuid())`} {
			if _, err = pool.Exec(ctx, sql); err == nil {
				t.Fatalf("unauthorized SQL: %s", sql)
			}
		}
	}
	if _, err = d.Pool.Exec(ctx, `SELECT * FROM rw_check_ownership`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Owner.Exec(ctx, `UPDATE rw_check_run SET nominal=999 WHERE id=$1`, id); err == nil {
		t.Fatal("immutable nominal changed")
	}
	if _, err = d.Owner.Exec(ctx, `UPDATE rw_check_grant SET version=2 WHERE id=$1`, g); err == nil {
		t.Fatal("immutable grant changed")
	}
	// Roll back the two later, empty budget functions first. The journal remains.
	migrateControlTo(t, d, 22)
	if err = db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateDown, logx.Discard()); err == nil {
		t.Fatal("rollback erased journal")
	}
	if err = db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateUp, logx.Discard()); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyControlMigrationRoundTrip(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	migrateControlTo(t, d, 21)
	if err := db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateUp, logx.Discard()); err != nil {
		t.Fatal(err)
	}
}

func migrateControlTo(t *testing.T, d *dbtest.DB, target int) {
	t.Helper()
	ctx := context.Background()
	for {
		var version int
		if err := d.Owner.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version <= target {
			return
		}
		if err := db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateDown, logx.Discard()); err != nil {
			t.Fatal(err)
		}
	}
}
