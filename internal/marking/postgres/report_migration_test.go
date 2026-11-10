package postgres

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/migrations"
)

func TestLegacyInvoiceReceiptMigration(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	migrate := func(command string) error {
		return db.MigrateURL(ctx, f.d.OwnerURL, dbtest.AppRole, command, logx.Discard())
	}
	// This regression targets migration 17, independently of later modules.
	downLater := func() {
		files, err := fs.Glob(migrations.FS, "*.sql")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range files {
			version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
			if err != nil {
				t.Fatal(err)
			}
			if version > 17 {
				if err := migrate(db.MigrateDown); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	downLater()
	if err := migrate(db.MigrateDown); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := migrate(db.MigrateUp); err != nil {
			t.Error(err)
		}
	})
	ct := domain.Contract{ID: ids.New(), Kind: domain.ContractInitial, ClientOrganizationID: f.org(), ContractorOrganizationID: f.org(), Sync: domain.NewSyncState(), CreatedAt: t0, UpdatedAt: t0}
	if _, _, err := f.s.Contracts().Insert(ctx, ct); err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	f.exec(`INSERT INTO ord_invoice (id,ord_contract_id,period,type,number,invoice_date,amount,sync_status,sync_request_id,sync_attempts,sent_at,created_at,updated_at)
 VALUES ($1,$2,'2026-10-01','act','legacy-test','2026-10-31',100,'sent','old-links-receipt',1,now(),now(),now())`, id, ct.ID)
	if err := migrate(db.MigrateUp); err != nil {
		t.Fatal(err)
	}
	var uncertain bool
	var receipt *string
	if err := f.d.Pool.QueryRow(ctx, `SELECT submission_uncertain,sync_request_id FROM ord_invoice WHERE id=$1`, id).Scan(&uncertain, &receipt); err != nil {
		t.Fatal(err)
	}
	if !uncertain || receipt != nil {
		t.Fatal("legacy document incorrectly confirmed")
	}
	if err := f.d.Pool.QueryRow(ctx, `SELECT sync_request_id FROM ord_invoice_creatives WHERE ord_invoice_id=$1`, id).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt == nil || *receipt != "old-links-receipt" {
		t.Fatal("legacy links receipt lost")
	}
	downLater()
	if err := migrate(db.MigrateDown); err == nil {
		t.Fatal("rollback erased receipts")
	}
	f.exec(`UPDATE ord_invoice SET submission_uncertain=false WHERE id=$1`, id)
	f.exec(`UPDATE ord_invoice_creatives SET sync_request_id=NULL,submission_uncertain=true WHERE ord_invoice_id=$1`, id)
	if err := migrate(db.MigrateDown); err == nil {
		t.Fatal("rollback erased uncertain links")
	}
}
