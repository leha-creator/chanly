package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestMigrationsDownAndUp(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	log := logx.Discard()
	// Откат всех миграций по одной: число миграций не зашито в тест.
	for step := 0; ; step++ {
		var version int64
		if err := d.Owner.QueryRow(ctx, `SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version == 0 {
			break
		}
		if step > 1000 {
			t.Fatalf("migrations did not roll back, version %d", version)
		}
		if err := db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateDown, log); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	var n int
	if err := d.Owner.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename <> 'goose_db_version'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d tables left after down", n)
	}
	if err := db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateUp, log); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := db.MigrateURL(ctx, d.OwnerURL, dbtest.AppRole, db.MigrateStatus, log); err != nil {
		t.Fatalf("status: %v", err)
	}
}

func TestImmutableTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	auditID := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO audit_log (id, actor_type, action, object_type, object_id, data, created_at)
		VALUES ($1, 'system', 'test.insert', 'test', 'x', '{}', $2)`, auditID, now); err != nil {
		t.Fatalf("app role cannot insert into audit_log: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE audit_log SET action = 'changed'`,
		`DELETE FROM audit_log`,
		`UPDATE legal_acceptance SET ip = NULL`,
		`DELETE FROM legal_acceptance`,
	} {
		_, err := d.Pool.Exec(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE "user" SET status = 'active' WHERE false`); err != nil {
		t.Fatalf("app role cannot update user: %v", err)
	}
}

func TestInTxRollsBack(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	runner := tx.NewRunner(d.Pool, logx.Discard())
	insert := func(ctx context.Context) error {
		_, err := db.Conn(ctx, d.Pool).Exec(ctx, `INSERT INTO job (id, kind, payload, run_at, status, max_attempts, created_at, updated_at)
			VALUES ($1, 'test', '{}', now(), 'pending', 1, now(), now())`, ids.New())
		return err
	}
	boom := errors.New("boom")
	err := runner.InTx(ctx, func(ctx context.Context) error {
		if err := insert(ctx); err != nil {
			return err
		}
		// Вложенный InTx работает в той же транзакции и откатывается вместе с ней.
		if err := runner.InTx(ctx, insert); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx error = %v", err)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM job`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows survived rollback", n)
	}

	if err := runner.InTx(ctx, insert); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM job`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("commit: n=%d err=%v", n, err)
	}
}

func TestUniqueViolation(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	q := `INSERT INTO "user" (id, email, status, created_at, updated_at) VALUES ($1, 'A@example.ru', 'active', now(), now())`
	if _, err := d.Pool.Exec(ctx, q, ids.New()); err != nil {
		t.Fatal(err)
	}
	_, err := d.Pool.Exec(ctx, strings.Replace(q, "A@example.ru", "a@EXAMPLE.ru", 1), ids.New())
	if !db.UniqueViolation(err, "user_email_key") {
		t.Fatalf("email must be unique case-insensitively, got %v", err)
	}
}

func TestIntegrationTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO outbound_request (id, system, operation, attempt, request, created_at) VALUES ('` + ids.New().String() + `', 'max', 'GetMe', 1, '{}', now())`,
		`INSERT INTO rate_limit_bucket (key, tokens, refilled_at) VALUES ('max:global', 25, now())`,
		`UPDATE rate_limit_bucket SET tokens = 1`,
		`INSERT INTO outbound_circuit (system, updated_at) VALUES ('max', now())`,
		`INSERT INTO alert (id, reason, level, summary, first_raised_at, last_raised_at, raised_count) VALUES ('` + ids.New().String() + `', 'test', 'error', 's', now(), now(), 1)`,
		`INSERT INTO worker_heartbeat (instance, started_at, seen_at) VALUES ('w1', now(), now())`,
		`DELETE FROM outbound_request`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	insert := `INSERT INTO inbound_event (id, source, external_key, event_type, payload, status, created_at, updated_at)
		VALUES ($1, 'max', 'k1', 'bot_added', '{}', 'received', now(), now()) ON CONFLICT (source, external_key) DO NOTHING`
	for range 2 {
		if _, err := d.Pool.Exec(ctx, insert, ids.New()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM inbound_event`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("inbound_event rows = %d, err = %v; want 1", n, err)
	}
}

func TestChannelTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	var categories int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM channel_category WHERE is_active`).Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if categories == 0 {
		t.Fatal("channel_category seed is empty")
	}
	insert := `INSERT INTO channel (id, max_chat_id, added_by_max_user_id, status, created_at, updated_at)
		VALUES ($1, $2, 7, $3, now(), now())`
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), -100, "unclaimed"); err != nil {
		t.Fatalf("app role cannot insert channel: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE channel SET title = 'x', lock_version = lock_version + 1`); err != nil {
		t.Fatalf("app role cannot update channel: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), -100, "unclaimed"); !db.UniqueViolation(err, "channel_max_chat_id_key") {
		t.Fatalf("max_chat_id must be unique, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), -101, "lost"); err == nil {
		t.Fatal("status outside the enum accepted")
	}
	// active без организации, категории и прав нарушает ограничение 04 §3.
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), -102, "active"); err == nil {
		t.Fatal("active channel without requirements accepted")
	}
}

// catalogChannel записывает канал, готовый к active: организация, категория, права.
func catalogChannel(t *testing.T, d *dbtest.DB, chatID int64) (channelID string) {
	t.Helper()
	ctx := context.Background()
	org := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO organization (id, legal_type, inn, name, contact_name, phone, status, created_at, updated_at)
		VALUES ($1, 'self_employed', $2, 'Блогер', 'Блогер', '+79990000000', 'active', now(), now())`,
		org, fmt.Sprintf("7%011d", -chatID)); err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO channel (id, organization_id, max_chat_id, added_by_max_user_id, status, is_public,
		category_id, bot_is_admin, bot_permissions, created_at, updated_at)
		VALUES ($1, $2, $3, 7, 'moderation', true, (SELECT id FROM channel_category ORDER BY position LIMIT 1), true,
		ARRAY['write', 'delete'], now(), now())`, id, org, chatID); err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func TestCatalogTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	var formats int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM placement_format WHERE is_active`).Scan(&formats); err != nil || formats != 3 {
		t.Fatalf("placement_format seed: %d formats, err %v", formats, err)
	}
	ch := catalogChannel(t, d, -200)

	// R-3: active без включённой цены не фиксируется — ошибка на COMMIT, а не на UPDATE.
	_, err := d.Pool.Exec(ctx, `UPDATE channel SET status = 'active' WHERE id = $1`, ch)
	if !db.CheckViolation(err, "channel_active_requires_price") {
		t.Fatalf("active without price: want channel_active_requires_price, got %v", err)
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE channel SET status = 'active' WHERE id = $1`, ch); err != nil {
		t.Fatalf("deferred check fired before commit: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
		VALUES ($1, $2, 'f_1_24', 300000, true, now(), now())`, ids.New(), ch); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("active with price: %v", err)
	}

	// Выключение и удаление последней цены активного канала не фиксируются.
	for _, stmt := range []string{
		`UPDATE channel_price SET is_enabled = false WHERE channel_id = $1`,
		`DELETE FROM channel_price WHERE channel_id = $1`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt, ch); !db.CheckViolation(err, "channel_active_requires_price") {
			t.Errorf("%s: want channel_active_requires_price, got %v", stmt, err)
		}
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO channel_price (id, channel_id, format_id, amount, is_enabled, created_at, updated_at)
		VALUES ($1, $2, 'f_2_48', 9999, true, now(), now())`, ids.New(), ch); err == nil {
		t.Error("price below 100 ₽ accepted")
	}

	// Интервалы недоступности: роль пишет и удаляет, конец не раньше начала.
	if _, err := d.Pool.Exec(ctx, `INSERT INTO channel_unavailability (id, channel_id, starts_at, ends_at, created_at)
		VALUES ($1, $2, now(), now() + interval '1 day', now())`, ids.New(), ch); err != nil {
		t.Fatalf("app role cannot insert unavailability: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO channel_unavailability (id, channel_id, starts_at, ends_at, created_at)
		VALUES ($1, $2, now(), now(), now())`, ids.New(), ch); err == nil {
		t.Error("empty unavailability interval accepted")
	}
	if _, err := d.Pool.Exec(ctx, `DELETE FROM channel_unavailability WHERE channel_id = $1`, ch); err != nil {
		t.Fatalf("app role cannot delete unavailability: %v", err)
	}

	// Снимок метрик: один на сутки, флаги — из перечня.
	snap := `INSERT INTO channel_metrics (id, channel_id, date, subscribers_count, anomaly_flags, created_at, updated_at)
		VALUES ($1, $2, '2026-10-16', 100, $3, now(), now())`
	if _, err := d.Pool.Exec(ctx, snap, ids.New(), ch, []string{"subscribers_spike"}); err != nil {
		t.Fatalf("app role cannot insert metrics: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, snap, ids.New(), ch, []string{}); !db.UniqueViolation(err, "channel_metrics_channel_id_date_key") {
		t.Fatalf("second snapshot for the same date: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, strings.Replace(snap, "2026-10-16", "2026-10-17", 1), ids.New(), ch, []string{"bogus"}); err == nil {
		t.Error("unknown anomaly flag accepted")
	}
}

// TestAdminTables — переписка по спору неизменяема; подтверждение — другим администратором (00014).
func TestAdminTables(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`UPDATE dispute_message SET text = 'x' WHERE false`,
		`DELETE FROM dispute_message WHERE false`,
		`DELETE FROM admin_approval WHERE false`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}
	admin := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO "user" (id, email, status, admin_role, created_at, updated_at)
		VALUES ($1, 'senior@example.ru', 'active', 'senior', now(), now())`, admin); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO admin_approval (id, kind, payload, amount, summary, reason, idempotency_key, requested_by,
		status, decided_by, decided_at, result_id, expires_at, created_at, updated_at)
		VALUES ($1, 'ledger_manual', '{}', 100, 'bank → platform_revenue', 'test', $2, $3, $4, $5, $6, $7, now(), now(), now())`
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), "k1", admin, "pending", nil, nil, nil); err != nil {
		t.Fatalf("pending approval: %v", err)
	}
	now := time.Now()
	_, err := d.Pool.Exec(ctx, insert, ids.New(), "k2", admin, "approved", admin, now, ids.New())
	if err == nil || !strings.Contains(err.Error(), "admin_approval_other_admin") {
		t.Errorf("approval by the same admin: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insert, ids.New(), "k1", admin, "pending", nil, nil, nil); !db.UniqueViolation(err, "admin_approval_idempotency_key_key") {
		t.Errorf("duplicate idempotency key: %v", err)
	}
}

// TestOrdReportingTables — снимки показов неизменяемы, отчёт месяца не удаляется; период —
// первое число месяца (00015).
func TestOrdReportingTables(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`UPDATE publication_views_snapshot SET views = 1 WHERE false`,
		`DELETE FROM publication_views_snapshot WHERE false`,
		`DELETE FROM ord_report WHERE false`,
		`TRUNCATE ord_statistics`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}
	for _, stmt := range []string{`DELETE FROM ord_invoice_item WHERE false`, `DELETE FROM ord_statistics WHERE false`} {
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			t.Errorf("%s: %v", stmt, err)
		}
	}
	_, err := d.Pool.Exec(ctx, `INSERT INTO ord_report (period, built_at, updated_at) VALUES ('2026-09-15', now(), now())`)
	if err == nil || !strings.Contains(err.Error(), "ord_report_period_check") {
		t.Errorf("period not on the first day: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO ord_report (period, built_at, updated_at) VALUES ('2026-09-01', now(), now())`); err != nil {
		t.Fatal(err)
	}
}
