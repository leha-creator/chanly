package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestMoneyTablesForAppRole(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	org, user, _, version, _, _, placement := orderFixture(t, d)

	insertAccount := `INSERT INTO ledger_account (id, kind, organization_id, placement_id, balance, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())`
	bank, balance := ids.New(), ids.New()
	if _, err := d.Pool.Exec(ctx, insertAccount, bank, "bank", nil, nil, 0); err != nil {
		t.Fatalf("app role cannot insert ledger_account: %v", err)
	}
	// Один счёт без разреза на вид: пустые значения не различаются.
	if _, err := d.Pool.Exec(ctx, insertAccount, ids.New(), "bank", nil, nil, 0); !db.UniqueViolation(err, "ledger_account_key") {
		t.Fatalf("second bank account: want unique violation, got %v", err)
	}
	// Разрез соответствует виду.
	if _, err := d.Pool.Exec(ctx, insertAccount, ids.New(), "advertiser_balance", nil, nil, 0); !db.CheckViolation(err, "ledger_account_scope") {
		t.Fatalf("advertiser_balance without organization: want check violation, got %v", err)
	}
	// 06 §2.2 п. 4: пассив не уходит в минус, актив — может.
	if _, err := d.Pool.Exec(ctx, insertAccount, ids.New(), "advertiser_balance", org, nil, -1); !db.CheckViolation(err, "ledger_account_liability_balance") {
		t.Fatalf("negative liability: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertAccount, balance, "advertiser_balance", org, nil, 0); err != nil {
		t.Fatalf("advertiser_balance: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE ledger_account SET balance = -100 WHERE id = $1`, bank); err != nil {
		t.Fatalf("negative asset must be allowed: %v", err)
	}

	txID := ids.New()
	insertTx := `INSERT INTO ledger_transaction (id, kind, actor_type, actor_user_id, reason, idempotency_key, entries_hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'h', now())`
	if _, err := d.Pool.Exec(ctx, insertTx, txID, "manual", "admin", user, "исправление", "manual:1"); err != nil {
		t.Fatalf("app role cannot insert ledger_transaction: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertTx, ids.New(), "manual", "admin", user, "ещё", "manual:1"); !db.UniqueViolation(err, "ledger_transaction_idempotency_key_key") {
		t.Fatalf("duplicate key: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertTx, ids.New(), "manual", "admin", user, nil, "manual:2"); !db.CheckViolation(err, "ledger_transaction_reason_required") {
		t.Fatalf("manual without reason: want check violation, got %v", err)
	}

	insertEntry := `INSERT INTO ledger_entry (id, ledger_transaction_id, position, debit_account_id, credit_account_id, amount, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())`
	if _, err := d.Pool.Exec(ctx, insertEntry, ids.New(), txID, 0, bank, balance, 100); err != nil {
		t.Fatalf("app role cannot insert ledger_entry: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertEntry, ids.New(), txID, 1, bank, bank, 100); !db.CheckViolation(err, "ledger_entry_accounts_differ") {
		t.Fatalf("same accounts: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertEntry, ids.New(), txID, 2, bank, balance, 0); err == nil {
		t.Fatal("zero amount entry must be rejected")
	}
	// 06 §2.2 п. 5: журнал только на добавление.
	for _, stmt := range []string{
		`UPDATE ledger_entry SET amount = 1`,
		`DELETE FROM ledger_entry`,
		`UPDATE ledger_transaction SET reason = 'x'`,
		`DELETE FROM ledger_transaction`,
		`DELETE FROM ledger_account`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}

	// I-03: одно активное удержание на размещение.
	insertHold := `INSERT INTO hold (id, placement_id, post_version_id, organization_id, amount, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1150000, 'active', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertHold, ids.New(), placement, version, org); err != nil {
		t.Fatalf("app role cannot insert hold: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertHold, ids.New(), placement, version, org); !db.UniqueViolation(err, "hold_active_placement_key") {
		t.Fatalf("second active hold: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE hold SET status = 'released' WHERE placement_id = $1`, placement); !db.CheckViolation(err, "hold_closed_at") {
		t.Fatalf("released without closed_at: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE hold SET status = 'released', closed_at = now() WHERE placement_id = $1`, placement); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertHold, ids.New(), placement, version, org); err != nil {
		t.Fatalf("new hold after release: %v", err)
	}

	// Счёт: номер из последовательности начинается с 1.
	var seq int64
	if err := d.Pool.QueryRow(ctx, `SELECT nextval('invoice_number_seq')`).Scan(&seq); err != nil || seq != 1 {
		t.Fatalf("invoice_number_seq = %d, %v; want 1", seq, err)
	}
	invoiceID := ids.New()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO invoice (id, number, organization_id, amount, status, expires_at, payer_name, payer_inn,
		pdf_storage_key, created_by, created_at, updated_at)
		VALUES ($1, 'MX-000001', $2, 2000000, 'issued', now() + interval '14 days', 'ООО Ромашка', '7707083893', 'k', $3, now(), now())`,
		invoiceID, org, user); err != nil {
		t.Fatalf("app role cannot insert invoice: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE invoice SET status = 'paid' WHERE id = $1`, invoiceID); !db.CheckViolation(err, "invoice_paid_pair") {
		t.Fatalf("paid without paid_at: want check violation, got %v", err)
	}

	statementID := ids.New()
	insertStatement := `INSERT INTO bank_statement (id, uploaded_by, storage_key, file_sha256, account_number, created_at, updated_at)
		VALUES ($1, $2, 'k', 'sha', '40802810000000000001', now(), now())`
	if _, err := d.Pool.Exec(ctx, insertStatement, statementID, user); err != nil {
		t.Fatalf("app role cannot insert bank_statement: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertStatement, ids.New(), user); !db.UniqueViolation(err, "bank_statement_file_sha256_key") {
		t.Fatalf("same file: want unique violation, got %v", err)
	}
	insertBankTx := `INSERT INTO bank_transaction (id, bank_statement_id, external_id, operation_date, amount, match_status,
		organization_id, created_at, updated_at) VALUES ($1, $2, $3, '2026-10-20', 100000, $4, $5, now(), now())`
	if _, err := d.Pool.Exec(ctx, insertBankTx, ids.New(), statementID, "op-1", "matched", org); err != nil {
		t.Fatalf("app role cannot insert bank_transaction: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertBankTx, ids.New(), statementID, "op-1", "unmatched", nil); !db.UniqueViolation(err, "bank_transaction_external_id_key") {
		t.Fatalf("same operation: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertBankTx, ids.New(), statementID, "op-2", "matched", nil); !db.CheckViolation(err, "bank_transaction_matched_org") {
		t.Fatalf("matched without organization: want check violation, got %v", err)
	}

	insertAct := `INSERT INTO closing_act (id, organization_id, period, number, placements_amount, fee_amount,
		report_pdf_storage_key, act_pdf_storage_key, created_at) VALUES ($1, $2, $3, $4, 1, 1, 'r', 'a', now())`
	if _, err := d.Pool.Exec(ctx, insertAct, ids.New(), org, "2026-10-01", "А-000001"); err != nil {
		t.Fatalf("app role cannot insert closing_act: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertAct, ids.New(), org, "2026-10-01", "А-000002"); !db.UniqueViolation(err, "closing_act_organization_id_period_key") {
		t.Fatalf("second act of period: want unique violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, insertAct, ids.New(), org, "2026-11-02", "А-000003"); err == nil {
		t.Fatal("period not on the first day must be rejected")
	}

	for _, stmt := range []string{
		`DELETE FROM hold`, `DELETE FROM invoice`, `DELETE FROM bank_transaction`, `DELETE FROM bank_statement`, `DELETE FROM closing_act`,
	} {
		if _, err := d.Pool.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", stmt, err)
		}
	}

	// I-12: accepted без сходящегося распределения не сохраняется.
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET status = 'accepted' WHERE id = $1`, placement); !db.CheckViolation(err, "placement_distribution_sum") {
		t.Fatalf("accepted without distribution: want check violation, got %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE placement SET status = 'accepted', performance_share = 7500,
		payout_amount = 750000, fee_charged = 112500, refund_amount = 287500 WHERE id = $1`, placement); err != nil {
		t.Fatalf("accepted with distribution: %v", err)
	}
}
