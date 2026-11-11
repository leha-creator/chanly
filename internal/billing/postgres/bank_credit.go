package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

var _ domain.BankCreditBudget = (*Store)(nil)

func (s *Store) ReserveBankCredit(ctx context.Context, p domain.BankCreditPolicy, id uuid.UUID, amount money.Kopecks, now time.Time) error {
	if _, ok := tx.FromContext(ctx); !ok {
		return errors.New("bank credit reservation requires transaction")
	}
	if err := p.Check(p.OrganizationID, amount, now); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	q := s.q(ctx)
	if err = q.CreateBankCreditPolicy(ctx, sqlc.CreateBankCreditPolicyParams{RunID: p.RunID, PolicySha256: hash, AccountNumber: p.Account, OrganizationID: p.OrganizationID, MaxPayment: int64(p.MaxPayment), MaxTotal: int64(p.MaxTotal), ExpiresAt: p.ExpiresAt}); err != nil {
		return err
	}
	policy, err := q.LockBankCreditPolicy(ctx, p.RunID)
	if err != nil {
		return err
	}
	if policy.PolicySha256 != hash {
		return domain.DenyBankCredit("policy_changed")
	}
	old, err := q.FindBankCreditSpend(ctx, id)
	if err == nil {
		if old.RunID == p.RunID && old.Amount == int64(amount) {
			return nil
		}
		return domain.DenyBankCredit("already_reserved_in_another_run")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if int64(amount) > policy.MaxTotal-policy.Spent {
		return domain.DenyBankCredit("total_limit")
	}
	if err = q.AddBankCreditSpend(ctx, sqlc.AddBankCreditSpendParams{BankTransactionID: id, RunID: p.RunID, Amount: int64(amount), CreatedAt: now}); err != nil {
		return err
	}
	if err = q.IncreaseBankCreditSpent(ctx, sqlc.IncreaseBankCreditSpentParams{RunID: p.RunID, Amount: int64(amount)}); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "controlled bank credit reserved", "run_id", p.RunID, "bank_transaction_id", id, "amount", int64(amount), "remaining", policy.MaxTotal-policy.Spent-int64(amount))
	return nil
}
