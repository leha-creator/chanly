package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/billing/postgres/sqlc"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/db"
)

func (s *Store) ReserveInvoiceDocument(ctx context.Context, invoiceID, orgID uuid.UUID, doc domain.InvoiceDoc) error {
	snapshot, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	err = s.q(ctx).ReserveInvoiceDocument(ctx, sqlc.ReserveInvoiceDocumentParams{InvoiceID: invoiceID, OrganizationID: orgID, RequestSnapshot: snapshot})
	if db.UniqueViolation(err, "bank_invoice_document_pending") {
		return apperr.Conflict("previous bank invoice creation requires verification")
	}
	return err
}

func (s *Store) SaveInvoiceDocument(ctx context.Context, id uuid.UUID, doc domain.BankInvoiceDocument) error {
	n, err := s.q(ctx).SaveInvoiceDocument(ctx, sqlc.SaveInvoiceDocumentParams{InvoiceID: id, RemoteID: &doc.ID, PdfUrl: doc.PDFURL})
	if err == nil && n != 1 {
		return apperr.Conflict("bank invoice document already recorded")
	}
	return err
}

func (s *Store) InvoiceDocument(ctx context.Context, id uuid.UUID) (domain.BankInvoiceDocument, error) {
	r, err := s.q(ctx).GetInvoiceDocument(ctx, id)
	if err != nil {
		return domain.BankInvoiceDocument{}, s.fail(ctx, "InvoiceDocument", err)
	}
	return domain.BankInvoiceDocument{ID: uuidOf(r.RemoteID), PDFURL: r.PdfUrl}, nil
}

func (s *Store) PendingInvoiceDocuments(ctx context.Context) ([]domain.InvoiceDocumentRef, error) {
	rows, err := s.q(ctx).PendingInvoiceDocuments(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.InvoiceDocumentRef, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.InvoiceDocumentRef{InvoiceID: row.InvoiceID, OrganizationID: row.OrganizationID})
	}
	return result, nil
}

func (s *Store) ArchiveInvoiceDocument(ctx context.Context, id uuid.UUID) error {
	return s.q(ctx).ArchiveInvoiceDocument(ctx, id)
}
