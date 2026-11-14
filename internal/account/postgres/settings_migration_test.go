package postgres_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestSettingsMigrationRoundTripWithSuppressedDelivery(t *testing.T) {
	st, d := newStore(t)
	u := createUser(t, st, "migration-settings@example.test")
	id := uuid.New()
	_, err := d.Pool.Exec(t.Context(), `INSERT INTO notification(id,user_id,channel,template,status,created_at,updated_at) VALUES($1,$2,'email','balance.deposited','suppressed',$3,$3)`, id, u.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// Roll back newer migrations too: this test targets the settings migration,
	// rather than assuming it will always be the latest migration.
	for {
		if err = db.MigrateURL(t.Context(), d.OwnerURL, dbtest.AppRole, db.MigrateDown, logx.Discard()); err != nil {
			t.Fatal(err)
		}
		var settingsExist bool
		if err = d.Owner.QueryRow(t.Context(), "SELECT to_regclass('user_settings') IS NOT NULL").Scan(&settingsExist); err != nil {
			t.Fatal(err)
		}
		if !settingsExist {
			break
		}
	}
	var status string
	if err = d.Pool.QueryRow(t.Context(), `SELECT status FROM notification WHERE id=$1`, id).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("down status=%s err=%v", status, err)
	}
	var exists bool
	if err = d.Owner.QueryRow(t.Context(), `SELECT to_regclass('user_settings') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("settings table remains after down")
	}
	if err = db.MigrateURL(t.Context(), d.OwnerURL, dbtest.AppRole, db.MigrateUp, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(t.Context(), `UPDATE notification SET status='suppressed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
