package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// orderFixture записывает рекламодателя с кампанией, постом и версией, канал блогера,
// заказ и размещение в created.
func orderFixture(t *testing.T, d *dbtest.DB) (org, user, campaign, version, channel, order, placement string) {
	t.Helper()
	ctx := context.Background()
	org, user, _, _, post := mediaPostFixture(t, d)
	if err := d.Pool.QueryRow(ctx, `SELECT campaign_id::text FROM post WHERE id = $1`, post).Scan(&campaign); err != nil {
		t.Fatal(err)
	}
	channel = catalogChannel(t, d, -500)
	versionID, orderID, placementID := ids.New(), ids.New(), ids.New()
	steps := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO post_version (id, post_id, number, status, content_doc, body, token_carrier, token_position,
			advertiser_name, advertiser_inn, created_by, created_at)
			VALUES ($1, $2, 1, 'pending_moderation', '{}', 'текст', 'text', 'below', 'ООО Ромашка', '7707083893', $3, now())`,
			[]any{versionID, post, user}},
		{`INSERT INTO "order" (id, number, organization_id, campaign_id, post_id, current_post_version_id, status,
			total_amount, created_by, created_at, updated_at)
			VALUES ($1, nextval('order_number_seq'), $2, $3, $4, $5, 'moderation', 1150000, $6, now(), now())`,
			[]any{orderID, org, campaign, post, versionID, user}},
		{`INSERT INTO placement (id, order_id, channel_id, blogger_organization_id, format_id, top_hours, duration_hours,
			window_start, window_end, time_from, time_to, post_version_id, status, status_changed_at, fee_rate_bp,
			blogger_amount, fee_amount, total_amount, created_at, updated_at)
			VALUES ($1, $2, $3, (SELECT organization_id FROM channel WHERE id = $3), 'f_1_24', 1, 24,
			'2026-10-20 21:00:00+00', '2026-10-22 21:00:00+00', '09:00', '21:00', $4, 'created', now(), 1500,
			1000000, 150000, 1150000, now(), now())`,
			[]any{placementID, orderID, channel, versionID}},
	}
	for _, s := range steps {
		if _, err := d.Pool.Exec(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return org, user, campaign, versionID.String(), channel, orderID.String(), placementID.String()
}

func TestOrdersPlacementsTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	org, user, campaign, version, channel, order, placement := orderFixture(t, d)

	// R-21: номер заказа начинается с 1001.
	var number int64
	if err := d.Pool.QueryRow(ctx, `SELECT number FROM "order" WHERE id = $1`, order).Scan(&number); err != nil {
		t.Fatal(err)
	}
	if number != 1001 {
		t.Fatalf("first order number %d, want 1001", number)
	}

	// Корзина: второй позиции того же канала в организации нет (R-8).
	insertCart := `INSERT INTO cart_item (id, organization_id, channel_id, format_id, shown_total_amount, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, 'f_1_24', 1150000, $4, now(), now())`
	if _, err := d.Pool.Exec(ctx, insertCart, ids.New(), org, channel, user); err != nil {
		t.Fatalf("app role cannot insert cart_item: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertCart, ids.New(), org, channel, user); !db.UniqueViolation(err, "cart_item_organization_id_channel_id_key") {
		t.Fatalf("duplicate cart channel: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `DELETE FROM cart_item WHERE organization_id = $1`, org); err != nil {
		t.Fatalf("app role cannot delete cart_item: %v", err)
	}

	// D-16: второй заказ той же кампании не вставляется.
	var post string
	if err := d.Pool.QueryRow(ctx, `SELECT post_id::text FROM "order" WHERE id = $1`, order).Scan(&post); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO "order" (id, number, organization_id, campaign_id, post_id, current_post_version_id,
		status, total_amount, created_by, created_at, updated_at)
		VALUES ($1, nextval('order_number_seq'), $2, $3, $4, $5, 'moderation', 0, $6, now(), now())`,
		ids.New(), org, campaign, post, version, user); !db.UniqueViolation(err, "order_campaign_id_key") {
		t.Fatalf("second order of campaign: want unique violation, got %v", err)
	}

	// R-1: кампания в ordered — только со ссылкой на заказ.
	if _, err := d.Pool.Exec(ctx, `UPDATE campaign SET status = 'ordered' WHERE id = $1`, campaign); !db.CheckViolation(err, "campaign_ordered_has_order") {
		t.Fatalf("ordered campaign without order: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE campaign SET status = 'ordered', order_id = $2 WHERE id = $1`, campaign, order); err != nil {
		t.Fatalf("campaign to ordered: %v", err)
	}

	// I-05: суммы не меняются; сумма — цена блогера и комиссия.
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET blogger_amount = 900000, total_amount = 1050000 WHERE id = $1`,
		placement); !db.CheckViolation(err, "placement_amounts_immutable") {
		t.Fatalf("amount change: want placement_amounts_immutable, got %v", err)
	}

	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET fee_rate_bp = 2500 WHERE id = $1`, placement); !db.CheckViolation(err, "placement_amounts_immutable") {
		t.Fatalf("rate change: want placement_amounts_immutable, got %v", err)
	}

	// История: роль приложения только добавляет записи (04 §1).
	if _, err := d.Pool.Exec(ctx, `INSERT INTO placement_state_history (id, placement_id, transition, to_status, actor_type,
		actor_user_id, idempotency_key, created_at) VALUES ($1, $2, 'P-01', 'created', 'advertiser', $3, 'k1', now())`,
		ids.New(), placement, user); err != nil {
		t.Fatalf("app role cannot insert placement_state_history: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO placement_state_history (id, placement_id, transition, to_status, actor_type,
		idempotency_key, created_at) VALUES ($1, $2, 'P-06', 'cancelled', 'advertiser', 'k1', now())`,
		ids.New(), placement); !db.UniqueViolation(err, "placement_state_history_placement_id_idempotency_key_key") {
		t.Fatalf("repeated idempotency key: want unique violation, got %v", err)
	}
	for _, stmt := range []string{
		`UPDATE placement_state_history SET reason_code = 'x'`,
		`DELETE FROM placement_state_history`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}

	// I-04: из конечного состояния не выйти.
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET status = 'cancelled', lock_version = lock_version + 1 WHERE id = $1`, placement); err != nil {
		t.Fatalf("placement to cancelled: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET status = 'created' WHERE id = $1`, placement); !db.CheckViolation(err, "placement_terminal_guard") {
		t.Fatalf("leave terminal status: want placement_terminal_guard, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET status_reason = 'advertiser_cancel' WHERE id = $1`, placement); err != nil {
		t.Fatalf("terminal placement other fields: %v", err)
	}

	// Пара (заказ, канал) уникальна; scheduled без времени не бывает.
	if _, err := d.Pool.Exec(ctx, `INSERT INTO placement (id, order_id, channel_id, blogger_organization_id, format_id, top_hours,
		duration_hours, window_start, window_end, time_from, time_to, post_version_id, status, status_changed_at, fee_rate_bp,
		blogger_amount, fee_amount, total_amount, created_at, updated_at)
		SELECT $1, order_id, channel_id, blogger_organization_id, format_id, top_hours, duration_hours, window_start, window_end,
		time_from, time_to, post_version_id, 'created', now(), fee_rate_bp, blogger_amount, fee_amount, total_amount, now(), now()
		FROM placement WHERE id = $2`, ids.New(), placement); !db.UniqueViolation(err, "placement_order_id_channel_id_key") {
		t.Fatalf("second placement of channel in order: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO placement (id, order_id, channel_id, blogger_organization_id, format_id, top_hours,
		duration_hours, window_start, window_end, time_from, time_to, post_version_id, status, status_changed_at, fee_rate_bp,
		blogger_amount, fee_amount, total_amount, created_at, updated_at)
		SELECT $1, order_id, $3, blogger_organization_id, format_id, top_hours, duration_hours, window_start, window_end,
		time_from, time_to, post_version_id, 'scheduled', now(), fee_rate_bp, blogger_amount, fee_amount, 1, now(), now()
		FROM placement WHERE id = $2`, ids.New(), placement, catalogChannel(t, d, -501)); err == nil {
		t.Fatal("scheduled placement without publish_at and with wrong total accepted")
	}
}
