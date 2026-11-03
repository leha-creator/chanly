package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// InvoiceByNumber — счёт по номеру MX-… (служебные команды); нет — 404.
func (s *Service) InvoiceByNumber(ctx context.Context, number string) (domain.Invoice, error) {
	var inv domain.Invoice
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		inv, err = s.Invoices.ByNumberForUpdate(ctx, number)
		return err
	})
	return inv, notFound(err, "invoice")
}

// OrganizationByINN — организация по ИНН (ручное сопоставление, R-13); нет — 404.
func (s *Service) OrganizationByINN(ctx context.Context, inn string) (domain.OrgRequisites, error) {
	org, err := s.Organizations.ByINN(ctx, inn)
	return org, notFound(err, "organization")
}

// BankPosition — данные банка для сверки (06 §2.4): последняя выписка и несопоставленные
// поступления.
type BankPosition struct {
	// Latest — последняя выписка; nil — выписок нет.
	Latest         *domain.BankStatement
	UnmatchedCount int
	UnmatchedTotal money.Kopecks
}

// BankPosition читает данные банка для сверки.
func (s *Service) BankPosition(ctx context.Context) (BankPosition, error) {
	var p BankPosition
	latest, err := s.Statements.Latest(ctx)
	switch {
	case err == nil:
		p.Latest = &latest
	case !errors.Is(err, domain.ErrNotFound):
		return p, err
	}
	p.UnmatchedCount, p.UnmatchedTotal, err = s.BankTransactions.UnmatchedTotal(ctx)
	return p, err
}

// Операции администратора через HTTP (adm/payments, S-30).

// Statement — выписка по идентификатору (итог загрузки).
func (s *Service) Statement(ctx context.Context, id uuid.UUID) (domain.BankStatement, error) {
	st, err := s.Statements.Get(ctx, id)
	return st, notFound(err, "bank statement")
}

// LatestUploadedStatement — последняя загруженная выписка; нет — 404.
func (s *Service) LatestUploadedStatement(ctx context.Context) (domain.BankStatement, error) {
	st, err := s.Statements.LatestUploaded(ctx)
	return st, notFound(err, "bank statement")
}

// AdminBankTransactions — поступления со смещением, общим числом и номерами счетов.
func (s *Service) AdminBankTransactions(ctx context.Context, status domain.MatchStatus, offset, limit int) ([]domain.BankTransaction, map[uuid.UUID]domain.Invoice, int, error) {
	switch status {
	case "", domain.MatchMatched, domain.MatchUnmatched, domain.MatchIgnored:
	default:
		return nil, nil, 0, apperr.Validation("match_status", "invalid")
	}
	list, total, err := s.BankTransactions.AdminPage(ctx, status, offset, limit)
	if err != nil {
		return nil, nil, 0, err
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, bt := range list {
		if bt.InvoiceID != uuid.Nil {
			ids = append(ids, bt.InvoiceID)
		}
	}
	invoices, err := s.Invoices.ByIDs(ctx, ids)
	if err != nil {
		return nil, nil, 0, err
	}
	return list, invoices, total, nil
}

// BankTransactionInvoice — счёт поступления (ответ сопоставления); нет — пусто.
func (s *Service) BankTransactionInvoice(ctx context.Context, bt domain.BankTransaction) (map[uuid.UUID]domain.Invoice, error) {
	if bt.InvoiceID == uuid.Nil {
		return map[uuid.UUID]domain.Invoice{}, nil
	}
	return s.Invoices.ByIDs(ctx, []uuid.UUID{bt.InvoiceID})
}

// AdminInvoices — счета всех организаций со смещением и общим числом.
func (s *Service) AdminInvoices(ctx context.Context, f domain.AdminInvoiceFilter) ([]domain.Invoice, int, error) {
	return s.Invoices.AdminPage(ctx, f)
}
