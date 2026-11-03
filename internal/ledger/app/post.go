package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Result — итог проводки: идентификатор транзакции и признак повтора по ключу.
type Result struct {
	ID       uuid.UUID
	Replayed bool
}

// Post проводит транзакцию в транзакции базы вызывающего — единственный путь записи
// в журнал (07 §9). Порядок: проверка правила вида (R-4), блокировка счетов (R-5), повтор
// ключа (R-2), остатки (06 §2.2 п. 4), запись проводок и кеша (п. 6).
func (s *Service) Post(ctx context.Context, t domain.Transaction) (Result, error) {
	const op = "Ledger.Post"
	if err := t.Validate(); err != nil {
		s.log.ErrorContext(ctx, "ledger invalid posting", logx.KeyOp, op, KeyKind, t.Kind,
			KeyIdempotency, t.IdempotencyKey, logx.KeyError, err.Error())
		return Result{}, err
	}
	if !s.Store.InTransaction(ctx) {
		s.log.ErrorContext(ctx, "ledger called outside transaction", logx.KeyOp, op, KeyKind, t.Kind)
		return Result{}, domain.ErrNoTransaction
	}
	now := s.Clock.Now()
	accounts, err := s.Store.LockAccounts(ctx, t.Accounts(), now)
	if err != nil {
		return Result{}, err
	}

	prev, err := s.Store.TransactionByKey(ctx, t.IdempotencyKey)
	switch {
	case err == nil:
		if prev.EntriesHash != t.EntriesHash() {
			s.rejected(ctx, t, "key_conflict")
			return Result{}, fmt.Errorf("%w: %s", domain.ErrIdempotencyConflict, t.IdempotencyKey)
		}
		s.log.DebugContext(ctx, "ledger transaction replayed", logx.KeyOp, op, KeyKind, t.Kind,
			KeyTransactionID, prev.ID, KeyIdempotency, t.IdempotencyKey)
		return Result{ID: prev.ID, Replayed: true}, nil
	case !errors.Is(err, domain.ErrNotFound):
		return Result{}, err
	}

	balances := make(map[domain.AccountRef]money.Kopecks, len(accounts))
	for ref, a := range accounts {
		balances[ref] = a.Balance
	}
	next, err := domain.ApplyPostings(balances, t.Postings)
	if err != nil {
		var neg *domain.NegativeLiabilityError
		if errors.As(err, &neg) {
			s.rejected(ctx, t, "insufficient_balance", "missing", int64(neg.Missing), "account_kind", neg.Account.Kind)
		}
		return Result{}, err
	}

	id := ids.New()
	if err := s.Store.InsertTransaction(ctx, id, t, accounts, now); err != nil {
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			s.rejected(ctx, t, "key_conflict")
		}
		return Result{}, err
	}
	changed := make(map[uuid.UUID]money.Kopecks, len(accounts))
	for ref, a := range accounts {
		if next[ref] != a.Balance {
			changed[a.ID] = next[ref]
		}
	}
	if err := s.Store.SetBalances(ctx, changed, now); err != nil {
		return Result{}, err
	}
	s.Metrics.Posted(t.Kind)
	s.log.InfoContext(ctx, "ledger transaction posted", logx.KeyOp, op, KeyKind, t.Kind, KeyTransactionID, id,
		KeyIdempotency, t.IdempotencyKey, KeyAmount, int64(t.Total()), "postings", postingsForLog(t),
		"actor_type", t.Actor, KeyPlacementID, t.Basis.PlacementID)
	return Result{ID: id}, nil
}

// rejected — WARN об отклонённой проводке (06 §11).
func (s *Service) rejected(ctx context.Context, t domain.Transaction, reason string, extra ...any) {
	args := append([]any{logx.KeyOp, "Ledger.Post", KeyKind, t.Kind, KeyIdempotency, t.IdempotencyKey,
		KeyAmount, int64(t.Total()), "reason", reason}, extra...)
	s.log.WarnContext(ctx, "ledger transaction rejected", args...)
}
