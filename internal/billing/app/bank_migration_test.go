package app_test

import (
	"context"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestBankMigrationPreservesLegacyFileAndDeposit(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(500000)
	result := e.importStatement(statementFile(platformAccount, "legacy-file", payment{7, 500000, "7707083893", "Оплата по счету " + inv.Number}))
	ctx := context.Background()
	previous, err := e.svc.Statements.Get(ctx, result.StatementID)
	if err != nil {
		t.Fatal(err)
	}
	// No BankIdentity was wired in this fixture: the file predates the new source.
	for {
		var version int64
		if err = e.d.Owner.QueryRow(ctx, "SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version < 100 {
			break
		}
		if err = db.MigrateURL(ctx, e.d.OwnerURL, dbtest.AppRole, db.MigrateDown, logx.Discard()); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.MigrateURL(ctx, e.d.OwnerURL, dbtest.AppRole, db.MigrateUp, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	actual, err := e.svc.Statements.Get(ctx, result.StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Source != "file" || !actual.Complete || actual.UploadedBy != previous.UploadedBy || actual.StorageKey != previous.StorageKey || actual.FileSHA256 != previous.FileSHA256 {
		t.Fatalf("legacy statement changed: %+v", actual)
	}
	if e.balance(e.org) != 500000 || e.count("SELECT count(*) FROM ledger_transaction WHERE kind='deposit'") != 1 {
		t.Fatal("legacy deposit lost")
	}
}
