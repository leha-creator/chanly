package bootstrap

import (
	"context"

	"github.com/google/uuid"

	ledgerdomain "github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// testFunding — баланс рекламодателя из advertiserClient: оформление удерживает деньги (R-6).
const testFunding money.Kopecks = 100_000_000

// fund зачисляет amount на баланс организации пользователя email так, как это сделает
// сопоставление поступления из выписки (S-30).
func (a *testApp) fund(email string, amount money.Kopecks) uuid.UUID {
	a.t.Helper()
	ctx := context.Background()
	var userID, orgID uuid.UUID
	if err := a.db.Pool.QueryRow(ctx, `SELECT u.id, m.organization_id FROM "user" u
		JOIN organization_member m ON m.user_id = u.id WHERE u.email = $1`, email).Scan(&userID, &orgID); err != nil {
		a.t.Fatalf("fund %s: %v", email, err)
	}
	statement, bankTx := ids.New(), ids.New()
	if _, err := a.db.Pool.Exec(ctx, `INSERT INTO bank_statement (id, uploaded_by, storage_key, file_sha256, account_number,
		created_at, updated_at) VALUES ($1, $2, 'test', $3, '40802810000000000001', now(), now())`,
		statement, userID, statement.String()); err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.db.Pool.Exec(ctx, `INSERT INTO bank_transaction (id, bank_statement_id, external_id, operation_date, amount,
		match_status, organization_id, created_at, updated_at) VALUES ($1, $2, $3, '2026-10-20', $4, 'matched', $5, now(), now())`,
		bankTx, statement, bankTx.String(), int64(amount), orgID); err != nil {
		a.t.Fatal(err)
	}
	if err := a.mods.txRunner.InTx(ctx, func(ctx context.Context) error {
		_, err := a.mods.ledger.Post(ctx, ledgerdomain.NewDeposit(bankTx, uuid.Nil, orgID, amount, ledgerdomain.ActorSystem, uuid.Nil))
		return err
	}); err != nil {
		a.t.Fatalf("fund %s: %v", email, err)
	}
	return orgID
}
