package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/payout/postgres/sqlc"
)

func withdrawalOf(r sqlc.RocketworkWithdrawal) (domain.Withdrawal, error) {
	w := domain.Withdrawal{ID: r.ID, Scope: domain.ProviderScope{Environment: r.Environment, Account: r.AccountKey}, TaskID: r.TaskID, Hash: r.TermsHash, State: r.State}
	if json.Unmarshal(r.Terms, &w.Terms) != nil {
		return w, domain.ErrInvalidPayout
	}
	_, hash, err := w.Terms.Snapshot()
	if err != nil || hash != w.Hash {
		return w, domain.ErrInvalidPayout
	}
	return w, nil
}
func (s *Store) PrepareWithdrawal(ctx context.Context, w domain.Withdrawal) (domain.Withdrawal, error) {
	if err := w.Scope.Validate(); err != nil {
		return w, err
	}
	body, hash, err := w.Terms.Snapshot()
	if err != nil {
		return w, err
	}
	r, err := s.q(ctx).PrepareWithdrawal(ctx, sqlc.PrepareWithdrawalParams{ID: w.ID, Environment: w.Scope.Environment, AccountKey: w.Scope.Account, Terms: body, TermsHash: hash})
	if err != nil {
		return w, s.workflowError(ctx, "PrepareWithdrawal", err)
	}
	if r.TermsHash != hash || r.Environment != w.Scope.Environment || r.AccountKey != w.Scope.Account {
		return w, domain.ErrInvalidPayout
	}
	return withdrawalOf(r)
}
func (s *Store) Withdrawal(ctx context.Context, id uuid.UUID, lock bool) (domain.Withdrawal, error) {
	var r sqlc.RocketworkWithdrawal
	var err error
	if lock {
		r, err = s.q(ctx).LockWithdrawal(ctx, id)
	} else {
		r, err = s.q(ctx).GetWithdrawal(ctx, id)
	}
	if err != nil {
		return domain.Withdrawal{}, s.workflowError(ctx, "Withdrawal", err)
	}
	return withdrawalOf(r)
}
func (s *Store) ClaimWithdrawal(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := s.q(ctx).ClaimWithdrawal(ctx, id)
	if err != nil {
		return false, s.workflowError(ctx, "ClaimWithdrawal", err)
	}
	return n == 1, nil
}
func (s *Store) AttachWithdrawal(ctx context.Context, id uuid.UUID, taskID int64) error {
	n, err := s.q(ctx).AttachWithdrawal(ctx, sqlc.AttachWithdrawalParams{ID: id, TaskID: &taskID})
	if err != nil {
		return s.workflowError(ctx, "AttachWithdrawal", err)
	}
	if n != 1 {
		return domain.ErrConcurrentUpdate
	}
	return nil
}
func (s *Store) SettleWithdrawal(ctx context.Context, id uuid.UUID) error {
	n, err := s.q(ctx).SettleWithdrawal(ctx, id)
	if err != nil {
		return s.workflowError(ctx, "SettleWithdrawal", err)
	}
	if n != 1 {
		return domain.ErrConcurrentUpdate
	}
	return nil
}
