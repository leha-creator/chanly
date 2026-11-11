package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/ledger/app"
	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/ledgertest"
	"github.com/leha-creator/chanly/internal/ledger/postgres"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type env struct {
	t     *testing.T
	d     *dbtest.DB
	svc   *app.Service
	clock *clock.Manual
	org   uuid.UUID
	admin uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.New(t)
	log := logx.Discard()
	clk := clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	svc := app.New(app.Deps{
		Store: postgres.New(d.Pool, log),
		Tx:    tx.NewRunner(d.Pool, log),
		Clock: clk,
		Log:   log,
	})
	return &env{
		t: t, d: d, svc: svc, clock: clk,
		org:   ledgertest.Organization(t, d.Pool, accountdomain.LegalEntity, "Рекламодатель"),
		admin: ledgertest.User(t, d.Pool),
	}
}

// post проводит транзакцию в отдельной транзакции базы.
func (e *env) post(t domain.Transaction) (app.Result, error) {
	var res app.Result
	err := e.svc.Tx.InTx(context.Background(), func(ctx context.Context) error {
		var err error
		res, err = e.svc.Post(ctx, t)
		return err
	})
	return res, err
}

func (e *env) mustPost(t domain.Transaction) app.Result {
	e.t.Helper()
	res, err := e.post(t)
	if err != nil {
		e.t.Fatalf("post %s: %v", t.Kind, err)
	}
	return res
}

// deposit зачисляет amount на баланс рекламодателя org через фиктивную операцию банка.
func (e *env) deposit(org uuid.UUID, amount money.Kopecks) {
	e.t.Helper()
	bankTx := e.bankTransaction(org, amount)
	e.mustPost(domain.NewDeposit(bankTx, uuid.Nil, org, amount, domain.ActorSystem, uuid.Nil))
}

// bankTransaction записывает поступление из выписки (основание deposit).
func (e *env) bankTransaction(org uuid.UUID, amount money.Kopecks) uuid.UUID {
	e.t.Helper()
	ctx := context.Background()
	statement, id := uuid.New(), uuid.New()
	if _, err := e.d.Pool.Exec(ctx, `INSERT INTO bank_statement (id, uploaded_by, storage_key, file_sha256, account_number, created_at, updated_at)
		VALUES ($1, $2, 'k', $3, '40802810000000000001', now(), now())`, statement, e.admin, statement.String()); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.d.Pool.Exec(ctx, `INSERT INTO bank_transaction (id, bank_statement_id, external_id, operation_date, amount,
		match_status, organization_id, created_at, updated_at) VALUES ($1, $2, $3, '2026-10-20', $4, 'matched', $5, now(), now())`,
		id, statement, id.String(), int64(amount), org); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) balance(ref domain.AccountRef) money.Kopecks {
	e.t.Helper()
	b, err := postgres.New(e.d.Pool, logx.Discard()).AccountBalance(context.Background(), ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// converges — сумма активов равна сумме пассивов, кеш равен сумме проводок (06 §2.2 п. 3, 6).
func (e *env) converges() {
	e.t.Helper()
	ctx := context.Background()
	var assets, liabilities int64
	if err := e.d.Pool.QueryRow(ctx, `SELECT
		coalesce(sum(balance) FILTER (WHERE kind IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense')), 0),
		coalesce(sum(balance) FILTER (WHERE kind NOT IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense')), 0)
		FROM ledger_account`).Scan(&assets, &liabilities); err != nil {
		e.t.Fatal(err)
	}
	if assets != liabilities {
		e.t.Fatalf("assets %d != liabilities %d", assets, liabilities)
	}
	var mismatched int
	if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM ledger_account a WHERE a.balance <>
		CASE WHEN a.kind IN ('bank', 'rocketwork_deposit', 'payout_fees', 'diagnostic_expense') THEN 1 ELSE -1 END * (
			coalesce((SELECT sum(amount) FROM ledger_entry WHERE debit_account_id = a.id), 0) -
			coalesce((SELECT sum(amount) FROM ledger_entry WHERE credit_account_id = a.id), 0))`).Scan(&mismatched); err != nil {
		e.t.Fatal(err)
	}
	if mismatched != 0 {
		e.t.Fatalf("%d accounts with cached balance different from entries", mismatched)
	}
}
