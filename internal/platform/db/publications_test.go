package db_test

import (
	"context"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestPublicationsTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	_, _, _, _, channel, _, placement := orderFixture(t, d)

	insertPub := `INSERT INTO publication (id, placement_id, channel_id, max_message_id, published_at, content_hash,
		created_at, updated_at) VALUES ($1, $2, $3, $4, now(), 'h', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertPub, ids.New(), placement, channel, "mid.1"); err != nil {
		t.Fatalf("app role cannot insert publication: %v", err)
	}
	// I-11: вторая публикация размещения не вставляется.
	if _, err := d.Pool.Exec(ctx, insertPub, ids.New(), placement, channel, "mid.2"); !db.UniqueViolation(err, "publication_placement_id_key") {
		t.Fatalf("second publication: want unique violation, got %v", err)
	}
	// Причина снятия задаётся только вместе с временем.
	if _, err := d.Pool.Exec(ctx, `UPDATE publication SET removal_reason = 'schedule' WHERE placement_id = $1`, placement); err == nil {
		t.Fatal("removal_reason without removed_at accepted")
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE publication SET removed_at = now(), removal_reason = 'schedule', views = 10
		WHERE placement_id = $1`, placement); err != nil {
		t.Fatalf("app role cannot update publication: %v", err)
	}

	violationID, event := ids.New(), ids.New()
	insertViolation := `INSERT INTO violation (id, placement_id, kind, occurred_at, source, inbound_event_id, created_at)
		VALUES ($1, $2, 'top_violated', now(), 'event', $3, now())`
	if _, err := d.Pool.Exec(ctx, insertViolation, violationID, placement, event); err != nil {
		t.Fatalf("app role cannot insert violation: %v", err)
	}
	// R-10: повтор того же события не вставляется.
	if _, err := d.Pool.Exec(ctx, insertViolation, ids.New(), placement, event); !db.UniqueViolation(err, "violation_placement_event_key") {
		t.Fatalf("duplicate violation event: want unique violation, got %v", err)
	}
	// R-20: нарушения — только добавление.
	if _, err := d.Pool.Exec(ctx, `UPDATE violation SET kind = 'removed_early'`); err == nil {
		t.Fatal("app role updated violation")
	}
	if _, err := d.Pool.Exec(ctx, `DELETE FROM violation`); err == nil {
		t.Fatal("app role deleted violation")
	}

	insertDispute := `INSERT INTO dispute (id, placement_id, reason, opened_by_type, violation_id, deadline_at,
		suggested_share, created_at, updated_at) VALUES ($1, $2, 'top_violated', 'system', $3, now() + interval '72 hours',
		5000, now(), now())`
	if _, err := d.Pool.Exec(ctx, insertDispute, ids.New(), placement, violationID); err != nil {
		t.Fatalf("app role cannot insert dispute: %v", err)
	}
	// I-11: второй спор не вставляется.
	if _, err := d.Pool.Exec(ctx, insertDispute, ids.New(), placement, violationID); !db.UniqueViolation(err, "dispute_placement_id_key") {
		t.Fatalf("second dispute: want unique violation, got %v", err)
	}
	// Решение без доли и комментария не сохраняется.
	if _, err := d.Pool.Exec(ctx, `UPDATE dispute SET resolution = 'full_payout' WHERE placement_id = $1`, placement); err == nil {
		t.Fatal("incomplete dispute resolution accepted")
	}
}
