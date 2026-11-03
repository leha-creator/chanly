package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

type invoices struct{ s *Store }

var _ domain.Invoices = invoices{}

func invoiceFromRow(r sqlc.Invoice) domain.Invoice {
	return domain.Invoice{
		ID: r.ID, Number: r.Number, OrganizationID: r.OrganizationID, Amount: money.Kopecks(r.Amount),
		Status: domain.InvoiceStatus(r.Status), ExpiresAt: r.ExpiresAt.UTC(), PaidAt: utcPtr(r.PaidAt),
		PaidAmount: kopecksOf(r.PaidAmount),
		Payer: domain.Payer{
			Name: r.PayerName, INN: r.PayerInn, BankAccount: str(r.PayerBankAccount), BankName: str(r.PayerBankName),
			BankBIK: str(r.PayerBankBik), BankCorrAccount: str(r.PayerBankCorrAccount),
		},
		PDFStorageKey: r.PdfStorageKey, CreatedBy: uuidOf(r.CreatedBy), CancelReason: str(r.CancelReason),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func invoicesFromRows(rows []sqlc.Invoice) []domain.Invoice {
	out := make([]domain.Invoice, 0, len(rows))
	for _, r := range rows {
		out = append(out, invoiceFromRow(r))
	}
	return out
}

func (r invoices) NextNumber(ctx context.Context) (int64, error) {
	n, err := r.s.q(ctx).NextInvoiceNumber(ctx)
	if err != nil {
		return 0, r.s.fail(ctx, "Invoice.NextNumber", err)
	}
	return n, nil
}

func (r invoices) Insert(ctx context.Context, inv domain.Invoice) error {
	if err := r.s.q(ctx).InsertInvoice(ctx, sqlc.InsertInvoiceParams{
		ID: inv.ID, Number: inv.Number, OrganizationID: inv.OrganizationID, Amount: int64(inv.Amount),
		Status: string(inv.Status), ExpiresAt: inv.ExpiresAt, PaidAt: inv.PaidAt, PaidAmount: kopecksPtr(inv.PaidAmount),
		PayerName: inv.Payer.Name, PayerInn: inv.Payer.INN, PayerBankAccount: strPtr(inv.Payer.BankAccount),
		PayerBankName: strPtr(inv.Payer.BankName), PayerBankBik: strPtr(inv.Payer.BankBIK),
		PayerBankCorrAccount: strPtr(inv.Payer.BankCorrAccount), PdfStorageKey: inv.PDFStorageKey,
		CreatedBy: uuidPtr(inv.CreatedBy), CancelReason: strPtr(inv.CancelReason),
		CreatedAt: inv.CreatedAt, UpdatedAt: inv.UpdatedAt,
	}); err != nil {
		return r.s.fail(ctx, "Invoice.Insert", err)
	}
	r.s.log.DebugContext(ctx, "invoice saved", logx.KeyOp, "Invoice.Insert", "invoice_id", inv.ID)
	return nil
}

func (r invoices) Get(ctx context.Context, orgID, id uuid.UUID) (domain.Invoice, error) {
	row, err := r.s.q(ctx).GetInvoice(ctx, sqlc.GetInvoiceParams{ID: id, OrganizationID: orgID})
	if err != nil {
		return domain.Invoice{}, r.s.fail(ctx, "Invoice.Get", err)
	}
	return invoiceFromRow(row), nil
}

// GetForUpdate — счёт под блокировкой без ограничения организацией (администратор).
func (r invoices) GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Invoice, error) {
	row, err := r.s.q(ctx).GetInvoiceByIDForUpdate(ctx, id)
	if err != nil {
		return domain.Invoice{}, r.s.fail(ctx, "Invoice.GetForUpdate", err)
	}
	return invoiceFromRow(row), nil
}

func (r invoices) ByNumberForUpdate(ctx context.Context, number string) (domain.Invoice, error) {
	rows, err := r.ByNumbersForUpdate(ctx, []string{number})
	if err != nil {
		return domain.Invoice{}, err
	}
	if len(rows) == 0 {
		return domain.Invoice{}, domain.ErrNotFound
	}
	return rows[0], nil
}

func (r invoices) ByNumbersForUpdate(ctx context.Context, numbers []string) ([]domain.Invoice, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	rows, err := r.s.q(ctx).ListInvoicesByNumberForUpdate(ctx, numbers)
	if err != nil {
		return nil, r.s.fail(ctx, "Invoice.ByNumbersForUpdate", err)
	}
	return invoicesFromRows(rows), nil
}

func (r invoices) IssuedByINNAndAmountForUpdate(ctx context.Context, inn string, amount money.Kopecks) ([]domain.Invoice, error) {
	if inn == "" {
		return nil, nil
	}
	rows, err := r.s.q(ctx).ListIssuedInvoicesByINNAndAmountForUpdate(ctx, sqlc.ListIssuedInvoicesByINNAndAmountForUpdateParams{
		PayerInn: inn, Amount: int64(amount),
	})
	if err != nil {
		return nil, r.s.fail(ctx, "Invoice.IssuedByINNAndAmount", err)
	}
	return invoicesFromRows(rows), nil
}

func (r invoices) List(ctx context.Context, orgID uuid.UUID, after *domain.Cursor, limit int) ([]domain.Invoice, error) {
	has, at, id := cursorArgs(after)
	rows, err := r.s.q(ctx).ListInvoices(ctx, sqlc.ListInvoicesParams{
		OrganizationID: orgID, HasCursor: has, CursorAt: at, CursorID: id, Lim: int32(limit),
	})
	if err != nil {
		return nil, r.s.fail(ctx, "Invoice.List", err)
	}
	return invoicesFromRows(rows), nil
}

func (r invoices) DueForExpiry(ctx context.Context, now time.Time, limit int) ([]domain.Invoice, error) {
	rows, err := r.s.q(ctx).ListDueInvoicesForUpdate(ctx, sqlc.ListDueInvoicesForUpdateParams{Now: now, Lim: int32(limit)})
	if err != nil {
		return nil, r.s.fail(ctx, "Invoice.DueForExpiry", err)
	}
	return invoicesFromRows(rows), nil
}

func (r invoices) Update(ctx context.Context, inv domain.Invoice) error {
	n, err := r.s.q(ctx).UpdateInvoice(ctx, sqlc.UpdateInvoiceParams{
		ID: inv.ID, Status: string(inv.Status), PaidAt: inv.PaidAt, PaidAmount: kopecksPtr(inv.PaidAmount),
		CancelReason: strPtr(inv.CancelReason), UpdatedAt: inv.UpdatedAt,
	})
	if err != nil {
		return r.s.fail(ctx, "Invoice.Update", err)
	}
	if n == 0 {
		return fmt.Errorf("update invoice %s: %w", inv.ID, domain.ErrNotFound)
	}
	r.s.log.DebugContext(ctx, "invoice saved", logx.KeyOp, "Invoice.Update", "invoice_id", inv.ID, "status", inv.Status)
	return nil
}

// InvoiceNumbers — номера счетов по идентификаторам (описания операций баланса, модуль ledger).
func (s *Store) InvoiceNumbers(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q(ctx).ListInvoiceNumbers(ctx, ids)
	if err != nil {
		return nil, s.fail(ctx, "Invoice.Numbers", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Number
	}
	return out, nil
}

func (r invoices) AdminPage(ctx context.Context, f domain.AdminInvoiceFilter) ([]domain.Invoice, int, error) {
	q := r.s.q(ctx)
	rows, err := q.AdminListInvoices(ctx, sqlc.AdminListInvoicesParams{Status: string(f.Status), OrganizationID: f.OrganizationID,
		Number: f.Number, Off: int32(f.Offset), Lim: int32(f.Limit)})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "Invoice.AdminPage", err)
	}
	total, err := q.AdminCountInvoices(ctx, sqlc.AdminCountInvoicesParams{Status: string(f.Status), OrganizationID: f.OrganizationID,
		Number: f.Number})
	if err != nil {
		return nil, 0, r.s.fail(ctx, "Invoice.AdminCount", err)
	}
	return invoicesFromRows(rows), int(total), nil
}

func (r invoices) ByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Invoice, error) {
	out := make(map[uuid.UUID]domain.Invoice, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.s.q(ctx).ListInvoicesByIDs(ctx, ids)
	if err != nil {
		return nil, r.s.fail(ctx, "Invoice.ByIDs", err)
	}
	for _, inv := range invoicesFromRows(rows) {
		out[inv.ID] = inv
	}
	return out, nil
}
