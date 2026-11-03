// Package postgres реализует хранилище журнала проводок на PostgreSQL. Это единственный
// писатель ledger_account, ledger_transaction, ledger_entry и hold (07 §9). Запросы —
// в queries/*.sql, код доступа генерирует sqlc. Методы работают в транзакции из context.
package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/ledger/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Store — хранилище журнала.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New создаёт Store.
func New(pool *pgxpool.Pool, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log}
}

var _ domain.Store = (*Store)(nil)

func (s *Store) q(ctx context.Context) *sqlc.Queries {
	return sqlc.New(db.Conn(ctx, s.pool))
}

// fail переводит ошибку базы: «нет строк» — domain.ErrNotFound, повтор ключа —
// domain.ErrIdempotencyConflict (гонка двух транзакций с одним ключом), остальное
// пишется в журнал как непредвиденное и возвращается как есть.
func (s *Store) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case db.UniqueViolation(err, "ledger_transaction_idempotency_key_key"):
		return domain.ErrIdempotencyConflict
	}
	if !errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "repository call failed", logx.KeyOp, op, logx.KeyError, err.Error())
	}
	return err
}

// InTransaction — в context открыта транзакция.
func (s *Store) InTransaction(ctx context.Context) bool {
	t, ok := tx.FromContext(ctx)
	return ok && t != nil
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidVal(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func refOf(a sqlc.LedgerAccount) domain.AccountRef {
	return domain.AccountRef{
		Kind: domain.AccountKind(a.Kind), OrganizationID: uuidVal(a.OrganizationID), PlacementID: uuidVal(a.PlacementID),
	}
}

// TransactionByKey — транзакция по ключу идемпотентности.
func (s *Store) TransactionByKey(ctx context.Context, key string) (domain.Recorded, error) {
	row, err := s.q(ctx).GetLedgerTransactionByKey(ctx, key)
	if err != nil {
		return domain.Recorded{}, s.fail(ctx, "Ledger.TransactionByKey", err)
	}
	return domain.Recorded{
		ID: row.ID, Kind: domain.Kind(row.Kind), IdempotencyKey: row.IdempotencyKey,
		EntriesHash: row.EntriesHash, CreatedAt: row.CreatedAt.UTC(),
	}, nil
}

// LockAccounts создаёт недостающие счета и блокирует все в порядке id (R-5).
func (s *Store) LockAccounts(ctx context.Context, refs []domain.AccountRef, now time.Time) (map[domain.AccountRef]domain.Account, error) {
	q := s.q(ctx)
	accountIDs := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		if err := q.EnsureLedgerAccount(ctx, sqlc.EnsureLedgerAccountParams{
			ID: ids.New(), Kind: string(ref.Kind), OrganizationID: uuidPtr(ref.OrganizationID),
			PlacementID: uuidPtr(ref.PlacementID), Now: now,
		}); err != nil {
			return nil, s.fail(ctx, "Ledger.LockAccounts", err)
		}
		row, err := q.GetLedgerAccount(ctx, sqlc.GetLedgerAccountParams{
			Kind: string(ref.Kind), OrganizationID: uuidPtr(ref.OrganizationID), PlacementID: uuidPtr(ref.PlacementID),
		})
		if err != nil {
			return nil, s.fail(ctx, "Ledger.LockAccounts", err)
		}
		accountIDs = append(accountIDs, row.ID)
	}
	rows, err := q.LockLedgerAccounts(ctx, accountIDs)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.LockAccounts", err)
	}
	out := make(map[domain.AccountRef]domain.Account, len(rows))
	for _, r := range rows {
		ref := refOf(r)
		out[ref] = domain.Account{ID: r.ID, Ref: ref, Balance: money.Kopecks(r.Balance)}
	}
	s.log.DebugContext(ctx, "ledger accounts locked", logx.KeyOp, "Ledger.LockAccounts", "accounts", len(out))
	return out, nil
}

// InsertTransaction записывает транзакцию и проводки по порядку.
func (s *Store) InsertTransaction(ctx context.Context, id uuid.UUID, t domain.Transaction, accounts map[domain.AccountRef]domain.Account, now time.Time) error {
	q := s.q(ctx)
	if err := q.InsertLedgerTransaction(ctx, sqlc.InsertLedgerTransactionParams{
		ID: id, Kind: string(t.Kind), PlacementID: uuidPtr(t.Basis.PlacementID), InvoiceID: uuidPtr(t.Basis.InvoiceID),
		BankTransactionID: uuidPtr(t.Basis.BankTransactionID), PayoutID: uuidPtr(t.Basis.PayoutID),
		ActorType: string(t.Actor), ActorUserID: uuidPtr(t.ActorUserID), Reason: text(t.Reason),
		IdempotencyKey: t.IdempotencyKey, EntriesHash: t.EntriesHash(), CreatedAt: now,
	}); err != nil {
		return s.fail(ctx, "Ledger.InsertTransaction", err)
	}
	for i, p := range t.Postings {
		if err := q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{
			ID: ids.New(), LedgerTransactionID: id, Position: int16(i),
			DebitAccountID: accounts[p.Debit].ID, CreditAccountID: accounts[p.Credit].ID,
			Amount: int64(p.Amount), CreatedAt: now,
		}); err != nil {
			return s.fail(ctx, "Ledger.InsertTransaction", err)
		}
	}
	return nil
}

// SetBalances обновляет кеш остатков.
func (s *Store) SetBalances(ctx context.Context, balances map[uuid.UUID]money.Kopecks, now time.Time) error {
	q := s.q(ctx)
	for id, b := range balances {
		if err := q.SetLedgerAccountBalance(ctx, sqlc.SetLedgerAccountBalanceParams{ID: id, Balance: int64(b), Now: now}); err != nil {
			return s.fail(ctx, "Ledger.SetBalances", err)
		}
	}
	return nil
}

// AccountBalance — кеш остатка без блокировки; счёта нет — ноль.
func (s *Store) AccountBalance(ctx context.Context, ref domain.AccountRef) (money.Kopecks, error) {
	row, err := s.q(ctx).GetLedgerAccount(ctx, sqlc.GetLedgerAccountParams{
		Kind: string(ref.Kind), OrganizationID: uuidPtr(ref.OrganizationID), PlacementID: uuidPtr(ref.PlacementID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, s.fail(ctx, "Ledger.AccountBalance", err)
	}
	return money.Kopecks(row.Balance), nil
}

// BalancesByKind — сумма остатков по видам счетов.
func (s *Store) BalancesByKind(ctx context.Context) (map[domain.AccountKind]money.Kopecks, error) {
	rows, err := s.q(ctx).SumLedgerBalancesByKind(ctx)
	if err != nil {
		return nil, s.fail(ctx, "Ledger.BalancesByKind", err)
	}
	out := make(map[domain.AccountKind]money.Kopecks, len(rows))
	for _, r := range rows {
		out[domain.AccountKind(r.Kind)] = money.Kopecks(r.Balance)
	}
	return out, nil
}
