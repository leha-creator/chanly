package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestMarkingTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	org, _, _, version, channel, _, _ := orderFixture(t, d)
	var blogger string
	if err := d.Pool.QueryRow(ctx, `SELECT organization_id::text FROM channel WHERE id = $1`, channel).Scan(&blogger); err != nil {
		t.Fatal(err)
	}

	insertOrg := `INSERT INTO ord_organization (id, organization_id, is_rr, sync_status, created_at, updated_at)
		VALUES ($1, $2, false, 'pending', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertOrg, ids.New(), org); err != nil {
		t.Fatalf("app role cannot insert ord_organization: %v", err)
	}
	// Один контрагент на организацию.
	if _, err := d.Pool.Exec(ctx, insertOrg, ids.New(), org); !db.UniqueViolation(err, "ord_organization_organization_id_key") {
		t.Fatalf("second ord_organization: want unique violation, got %v", err)
	}
	// R-2: sent без времени отправки не сохраняется.
	if _, err := d.Pool.Exec(ctx, `UPDATE ord_organization SET sync_status = 'sent' WHERE organization_id = $1`, org); !db.CheckViolation(err, "ord_organization_sent_at") {
		t.Fatalf("sent without sent_at: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE ord_organization SET sync_status = 'sent', sent_at = now(), sync_request_id = 'r1'
		WHERE organization_id = $1`, org); err != nil {
		t.Fatalf("app role cannot update ord_organization: %v", err)
	}

	contractID := ids.New()
	insertContract := `INSERT INTO ord_contract (id, kind, client_organization_id, contractor_organization_id, sync_status, created_at, updated_at)
		VALUES ($1, 'initial', $2, $3, 'pending', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertContract, contractID, org, blogger); err != nil {
		t.Fatalf("app role cannot insert ord_contract: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertContract, ids.New(), org, blogger); !db.UniqueViolation(err, "ord_contract_parties_key") {
		t.Fatalf("second ord_contract: want unique violation, got %v", err)
	}

	insertPlatform := `INSERT INTO ord_platform (id, channel_id, organization_id, name, url, sync_status, created_at, updated_at)
		VALUES ($1, $2, $3, 'Канал', 'https://max.ru/c', 'pending', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertPlatform, ids.New(), channel, blogger); err != nil {
		t.Fatalf("app role cannot insert ord_platform: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertPlatform, ids.New(), channel, blogger); !db.UniqueViolation(err, "ord_platform_channel_id_key") {
		t.Fatalf("second ord_platform: want unique violation, got %v", err)
	}

	creativeID := ids.New()
	insertCreative := `INSERT INTO creative (id, post_version_id, ord_contract_id, erid, erid_received_at, sync_status, sent_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), 'sent', now(), now(), now())`
	if _, err := d.Pool.Exec(ctx, insertCreative, creativeID, version, contractID, "erid-1"); err != nil {
		t.Fatalf("app role cannot insert creative: %v", err)
	}
	// D-07: второй креатив версии не вставляется.
	if _, err := d.Pool.Exec(ctx, insertCreative, ids.New(), version, contractID, "erid-2"); !db.UniqueViolation(err, "creative_post_version_id_key") {
		t.Fatalf("second creative of version: want unique violation, got %v", err)
	}
	// Один erid — один креатив.
	version2 := nextVersion(t, d, version)
	if _, err := d.Pool.Exec(ctx, insertCreative, ids.New(), version2, contractID, "erid-1"); !db.UniqueViolation(err, "creative_erid_key") {
		t.Fatalf("duplicate erid: want unique violation, got %v", err)
	}
	// R-11: креатив без отправки удаляется.
	if _, err := d.Pool.Exec(ctx, `DELETE FROM creative WHERE id = $1`, creativeID); err != nil {
		t.Fatalf("app role cannot delete creative: %v", err)
	}
	// Остальные объекты ОРД не удаляются.
	for _, stmt := range []string{
		`DELETE FROM ord_contract`,
		`DELETE FROM ord_organization`,
		`DELETE FROM ord_platform`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}

	// R-14: новый канал не виден до регистрации площадки.
	var readyAt *string
	if err := d.Pool.QueryRow(ctx, `SELECT ord_platform_ready_at::text FROM channel WHERE id = $1`, channel).Scan(&readyAt); err != nil {
		t.Fatal(err)
	}
	if readyAt != nil {
		t.Fatalf("new channel ord_platform_ready_at = %v, want NULL", *readyAt)
	}
}

// nextVersion добавляет следующую версию к посту версии version.
func nextVersion(t *testing.T, d *dbtest.DB, version string) string {
	t.Helper()
	ctx := context.Background()
	id := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier,
		token_position, advertiser_name, advertiser_inn, created_by, created_at)
		SELECT $1, post_id, number + 1, 'pending_moderation', '{}', 'текст 2', 'text', 'below', advertiser_name,
		advertiser_inn, created_by, now() FROM post_version WHERE id = $2`, id, version); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return id.String()
}
