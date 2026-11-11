package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type statements struct{ s *Store }

var _ domain.Statements = statements{}

func statementFromRow(r sqlc.BankStatement) domain.BankStatement {
	return domain.BankStatement{
		ID: r.ID, UploadedBy: uuidOf(r.UploadedBy), StorageKey: str(r.StorageKey), FileSHA256: str(r.FileSha256), Account: r.AccountNumber, Source: r.Source, Complete: r.Complete,
		DateFrom: dateOf(r.DateFrom), DateTo: dateOf(r.DateTo), Opening: kopecksOf(r.OpeningBalance),
		Closing: kopecksOf(r.ClosingBalance), IncomingCount: int(r.IncomingCount), MatchedCount: int(r.MatchedCount),
		UnmatchedCount: int(r.UnmatchedCount), DuplicateCount: int(r.DuplicateCount), CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r statements) Insert(ctx context.Context, st domain.BankStatement) error {
	if err := r.s.q(ctx).InsertBankStatement(ctx, sqlc.InsertBankStatementParams{
		ID: st.ID, UploadedBy: uuidPtr(st.UploadedBy), StorageKey: strPtr(st.StorageKey), FileSha256: strPtr(st.FileSHA256), Source: statementSource(st.Source), Complete: st.Complete,
		AccountNumber: st.Account, DateFrom: pgDatePtr(st.DateFrom), DateTo: pgDatePtr(st.DateTo),
		OpeningBalance: kopecksPtr(st.Opening), ClosingBalance: kopecksPtr(st.Closing),
		IncomingCount: int32(st.IncomingCount), MatchedCount: int32(st.MatchedCount),
		UnmatchedCount: int32(st.UnmatchedCount), DuplicateCount: int32(st.DuplicateCount), CreatedAt: st.CreatedAt,
	}); err != nil {
		return r.s.fail(ctx, "Statement.Insert", err)
	}
	r.s.log.DebugContext(ctx, "bank statement saved", logx.KeyOp, "Statement.Insert", "bank_statement_id", st.ID)
	return nil
}

func (r statements) ExistsByHash(ctx context.Context, sha string) (bool, error) {
	found, err := r.s.q(ctx).BankStatementExists(ctx, &sha)
	if err != nil {
		return false, r.s.fail(ctx, "Statement.ExistsByHash", err)
	}
	return found, nil
}

func (r statements) UpdateCounts(ctx context.Context, st domain.BankStatement) error {
	if err := r.s.q(ctx).UpdateBankStatementCounts(ctx, sqlc.UpdateBankStatementCountsParams{
		ID: st.ID, IncomingCount: int32(st.IncomingCount),
		DuplicateCount: int32(st.DuplicateCount), UpdatedAt: st.UpdatedAt,
		OpeningBalance: kopecksPtr(st.Opening), ClosingBalance: kopecksPtr(st.Closing),
	}); err != nil {
		return r.s.fail(ctx, "Statement.UpdateCounts", err)
	}
	return nil
}

func (r statements) Latest(ctx context.Context) (domain.BankStatement, error) {
	row, err := r.s.q(ctx).LatestBankStatement(ctx)
	if err != nil {
		return domain.BankStatement{}, r.s.fail(ctx, "Statement.Latest", err)
	}
	return statementFromRow(row), nil
}

type bankTransactions struct{ s *Store }

var _ domain.BankTransactions = bankTransactions{}

func bankTransactionFromRow(r sqlc.BankTransaction) domain.BankTransaction {
	t := domain.BankTransaction{
		ID: r.ID, StatementID: r.BankStatementID,
		Incoming: domain.Incoming{
			ExternalID: r.ExternalID, Amount: money.Kopecks(r.Amount), PayerINN: str(r.PayerInn),
			PayerName: str(r.PayerName), PayerAccount: str(r.PayerAccount), Purpose: str(r.Purpose),
		},
		MatchStatus: domain.MatchStatus(r.MatchStatus), InvoiceID: uuidOf(r.InvoiceID), OrganizationID: uuidOf(r.OrganizationID),
		MatchedBy: uuidOf(r.MatchedBy), IgnoredReason: str(r.IgnoredReason), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if d := dateOf(r.OperationDate); d != nil {
		t.OperationDate = *d
	}
	return t
}

func (r bankTransactions) InsertIfNew(ctx context.Context, t domain.BankTransaction) (bool, error) {
	n, err := r.s.q(ctx).InsertBankTransaction(ctx, sqlc.InsertBankTransactionParams{
		ID: t.ID, BankStatementID: t.StatementID, ExternalID: t.ExternalID, OperationDate: pgDate(t.OperationDate),
		Amount: int64(t.Amount), PayerInn: strPtr(t.PayerINN), PayerName: strPtr(t.PayerName),
		PayerAccount: strPtr(t.PayerAccount), Purpose: strPtr(t.Purpose), MatchStatus: string(t.MatchStatus),
		InvoiceID: uuidPtr(t.InvoiceID), OrganizationID: uuidPtr(t.OrganizationID), MatchedBy: uuidPtr(t.MatchedBy),
		IgnoredReason: strPtr(t.IgnoredReason), CreatedAt: t.CreatedAt,
	})
	if err != nil {
		return false, r.s.fail(ctx, "BankTransaction.Insert", err)
	}
	return n == 1, nil
}

func (r bankTransactions) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.BankTransaction, error) {
	row, err := r.s.q(ctx).GetBankTransactionForUpdate(ctx, id)
	if err != nil {
		return domain.BankTransaction{}, r.s.fail(ctx, "BankTransaction.GetForUpdate", err)
	}
	return bankTransactionFromRow(row), nil
}

func (r bankTransactions) Update(ctx context.Context, t domain.BankTransaction) error {
	if err := r.s.q(ctx).UpdateBankTransaction(ctx, sqlc.UpdateBankTransactionParams{
		ID: t.ID, MatchStatus: string(t.MatchStatus), InvoiceID: uuidPtr(t.InvoiceID), OrganizationID: uuidPtr(t.OrganizationID),
		MatchedBy: uuidPtr(t.MatchedBy), IgnoredReason: strPtr(t.IgnoredReason), UpdatedAt: t.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, "BankTransaction.Update", err)
	}
	r.s.log.DebugContext(ctx, "bank transaction saved", logx.KeyOp, "BankTransaction.Update", "bank_transaction_id", t.ID,
		"match_status", t.MatchStatus)
	return nil
}

func (r bankTransactions) List(ctx context.Context, status domain.MatchStatus, after *domain.Cursor, limit int) ([]domain.BankTransaction, error) {
	has, at, id := cursorArgs(after)
	rows, err := r.s.q(ctx).ListBankTransactions(ctx, sqlc.ListBankTransactionsParams{
		MatchStatus: string(status), HasCursor: has, CursorAt: at, CursorID: id, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, "BankTransaction.List", err)
	}
	out := make([]domain.BankTransaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, bankTransactionFromRow(row))
	}
	return out, nil
}

func (r bankTransactions) UnmatchedTotal(ctx context.Context) (int, money.Kopecks, error) {
	row, err := r.s.q(ctx).UnmatchedBankTotal(ctx)
	if err != nil {
		return 0, 0, r.s.fail(ctx, "BankTransaction.UnmatchedTotal", err)
	}
	return int(row.N), money.Kopecks(row.Total), nil
}

func (r bankTransactions) AdminPage(ctx context.Context, status domain.MatchStatus, offset, limit int) ([]domain.BankTransaction, int, error) {
	q := r.s.q(ctx)
	rows, err := q.AdminListBankTransactions(ctx, sqlc.AdminListBankTransactionsParams{MatchStatus: string(status),
		Off: int32(offset), Lim: int32(limit)})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "BankTransaction.AdminPage", err)
	}
	total, err := q.AdminCountBankTransactions(ctx, string(status))
	if err != nil {
		return nil, 0, r.s.fail(ctx, "BankTransaction.AdminCount", err)
	}
	out := make([]domain.BankTransaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, bankTransactionFromRow(row))
	}
	return out, int(total), nil
}

func (r statements) Get(ctx context.Context, id uuid.UUID) (domain.BankStatement, error) {
	row, err := r.s.q(ctx).GetBankStatement(ctx, id)
	if err != nil {
		return domain.BankStatement{}, r.s.fail(ctx, "Statement.Get", err)
	}
	return statementFromRow(row), nil
}

func (r statements) LatestUploaded(ctx context.Context) (domain.BankStatement, error) {
	row, err := r.s.q(ctx).LatestUploadedBankStatement(ctx)
	if err != nil {
		return domain.BankStatement{}, r.s.fail(ctx, "Statement.LatestUploaded", err)
	}
	return statementFromRow(row), nil
}

func statementSource(source string) string {
	if source == "" {
		return "file"
	}
	return source
}
