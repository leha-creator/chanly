package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"strings"
	"time"
)

var _ domain.BankIdentity = (*Store)(nil)
var _ domain.BankSyncStore = (*Store)(nil)

func fingerprint(in domain.Incoming) string {
	raw, _ := json.Marshal([]any{in.Amount, in.PayerINN, in.PayerAccount, strings.TrimSpace(in.Purpose)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Store) ResolveIncoming(ctx context.Context, account string, in domain.Incoming) (domain.Incoming, bool, error) {
	if _, ok := tx.FromContext(ctx); !ok {
		return in, false, errors.New("bank identity requires transaction")
	}
	q := s.q(ctx)
	if err := q.LockBankIdentity(ctx, account); err != nil {
		return in, false, err
	}
	aliases := []string{"external:" + in.ExternalID}
	legacy := []string{in.ExternalID}
	if in.DocumentNumber != "" {
		dates := []time.Time{in.OperationDate}
		if in.ChargeDate != nil {
			dates = append(dates, *in.ChargeDate)
		}
		for _, d := range dates {
			key := clientbank.ExternalID(d, in.DocumentNumber, in.Amount, in.PayerINN, in.PayerAccount)
			legacy = append(legacy, key)
			aliases = append(aliases, "external:"+key)
		}
		if in.DocDate != nil {
			// Bank PDF/1C document dates use the Moscow calendar. T-API encodes
			// midnight of that day as 21:00Z on the previous UTC day.
			aliases = append(aliases, "doc:"+in.DocDate.In(time.FixedZone("MSK", 3*3600)).Format(time.DateOnly)+":"+in.DocumentNumber+":"+in.PayerINN+":"+in.PayerAccount)
		}
	}
	fp := fingerprint(in)
	found, err := q.FindBankAliases(ctx, sqlc.FindBankAliasesParams{AccountNumber: account, Aliases: aliases})
	if err != nil {
		return in, false, err
	}
	old, err := q.LegacyBankIncoming(ctx, sqlc.LegacyBankIncomingParams{AccountNumber: account, ExternalIds: legacy})
	if err != nil {
		return in, false, err
	}
	canonical := ""
	for _, f := range found {
		if canonical != "" && canonical != f.ExternalID {
			in.IdentityConflict = true
		}
		canonical = f.ExternalID
		if f.Fingerprint != fp {
			in.IdentityConflict = true
		}
	}
	for _, f := range old {
		if canonical != "" && canonical != f.ExternalID {
			in.IdentityConflict = true
		}
		canonical = f.ExternalID
		if fingerprint(bankTransactionFromRow(f).Incoming) != fp {
			in.IdentityConflict = true
		}
	}
	duplicate := canonical != ""
	if canonical == "" {
		canonical = in.ExternalID
	}
	// A conflict is recorded but never creates a second claim or deposit. The
	// original transaction remains available for reconciliation and its credit is retained.
	if !in.IdentityConflict {
		for _, a := range aliases {
			if err := q.SaveBankAlias(ctx, sqlc.SaveBankAliasParams{AccountNumber: account, Alias: a, ExternalID: canonical, Fingerprint: fp}); err != nil {
				return in, false, err
			}
		}
	}
	evidence, err := json.Marshal(in)
	if err != nil {
		return in, false, err
	}
	if err := q.ObserveBankIncoming(ctx, sqlc.ObserveBankIncomingParams{ID: ids.New(), AccountNumber: account, OperationID: in.BankOperationID, ExternalID: canonical, DocumentNumber: in.DocumentNumber, DocDate: in.DocDate, ChargeDate: in.ChargeDate, OperationDate: in.OperationDate, Conflict: in.IdentityConflict, StatementID: in.ImportID, Evidence: evidence}); err != nil {
		return in, false, err
	}
	if in.IdentityConflict {
		s.log.WarnContext(ctx, "bank identity requires reconciliation", "duplicate", duplicate)
	}
	in.ExternalID = canonical
	return in, duplicate || in.IdentityConflict, nil
}

func (s *Store) MissingBankOperations(ctx context.Context, account string, from, to time.Time, id uuid.UUID) (int, error) {
	n, err := s.q(ctx).MissingBankOperations(ctx, sqlc.MissingBankOperationsParams{AccountNumber: account, PeriodFrom: from, PeriodTo: to, StatementID: id})
	return int(n), err
}

// Session advisory lock spans network calls without a database transaction.
func (s *Store) WithBankLock(ctx context.Context, account string, fn func(context.Context) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", "bank-sync:"+account).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return domain.ErrBankSyncBusy
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", "bank-sync:"+account).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	return fn(ctx)
}
func (s *Store) LoadBankProgress(ctx context.Context, account string) (domain.BankProgress, error) {
	raw, err := s.q(ctx).LoadBankProgress(ctx, account)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BankProgress{Account: account}, nil
	}
	if err != nil {
		return domain.BankProgress{}, err
	}
	var p domain.BankProgress
	err = json.Unmarshal(raw, &p)
	return p, err
}
func (s *Store) SaveBankProgress(ctx context.Context, p domain.BankProgress) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.q(ctx).SaveBankProgress(ctx, sqlc.SaveBankProgressParams{AccountNumber: p.Account, Progress: raw})
}

func (r statements) ByHash(ctx context.Context, sha string) (domain.BankStatement, error) {
	row, err := r.s.q(ctx).BankStatementByHash(ctx, &sha)
	if err != nil {
		return domain.BankStatement{}, r.s.fail(ctx, "Statement.ByHash", err)
	}
	return statementFromRow(row), nil
}
func (r statements) Complete(ctx context.Context, id uuid.UUID) error {
	return r.s.q(ctx).CompleteBankStatement(ctx, id)
}
