package app

import (
	"bytes"
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
)

func bankDocumentUnavailable(id uuid.UUID) error {
	return apperr.New(apperr.CodeServiceUnavailable, "bank invoice document is unavailable; check the existing invoice before creating another").WithDetail("invoice_id", id.String())
}

func (s *Service) createBankInvoice(ctx context.Context, inv domain.Invoice, doc domain.InvoiceDoc) (domain.Invoice, error) {
	if s.InvoiceIssuer == nil || s.InvoiceDocuments == nil {
		return domain.Invoice{}, bankDocumentUnavailable(inv.ID)
	}
	// Commit local ownership before touching the bank. An interruption at any point
	// leaves a visible invoice and blocks further bank issuance for this organization.
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Invoices.Insert(ctx, inv); err != nil {
			return err
		}
		if err := s.InvoiceDocuments.ReserveInvoiceDocument(ctx, inv.ID, inv.OrganizationID, doc); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{Action: ActionInvoiceIssued, ObjectType: "invoice", ObjectID: inv.ID.String(), ActorType: audit.ActorAdvertiser, Data: map[string]any{"number": inv.Number, "amount": int64(inv.Amount), "provider": "tbank"}})
	})
	if err != nil {
		return domain.Invoice{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err := s.InvoiceIssuer.Send(callCtx, doc)
	if err != nil {
		s.log.ErrorContext(ctx, "bank invoice issuance requires verification", KeyInvoiceID, inv.ID)
		return domain.Invoice{}, bankDocumentUnavailable(inv.ID)
	}
	// Persist a successful response even when the browser disconnects.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer saveCancel()
	if err := s.InvoiceDocuments.SaveInvoiceDocument(saveCtx, inv.ID, remote); err != nil {
		s.log.ErrorContext(saveCtx, "bank invoice response could not be saved", KeyInvoiceID, inv.ID, "bank_invoice_id", remote.ID)
		return domain.Invoice{}, bankDocumentUnavailable(inv.ID)
	}
	pdf, err := s.InvoiceIssuer.Download(saveCtx, remote.PDFURL)
	if err != nil {
		// Creation succeeded. PDF GET may retry the safe download, never Send.
		s.log.WarnContext(saveCtx, "bank invoice PDF archival deferred", KeyInvoiceID, inv.ID)
		return inv, nil
	}
	if err := s.Storage.PutPrivate(saveCtx, inv.PDFStorageKey, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		s.log.WarnContext(saveCtx, "bank invoice PDF archival deferred", KeyInvoiceID, inv.ID)
	} else if err := s.InvoiceDocuments.ArchiveInvoiceDocument(saveCtx, inv.ID); err != nil {
		s.log.WarnContext(saveCtx, "bank invoice archive marker deferred", KeyInvoiceID, inv.ID)
	}
	return inv, nil
}

// ArchiveBankInvoiceDocuments retries only safe PDF reads; Send is never called.
// It continues after a provider switch so signed bank links do not expire first.
func (s *Service) ArchiveBankInvoiceDocuments(ctx context.Context) error {
	if s.InvoiceDocuments == nil || s.InvoiceIssuer == nil {
		return nil
	}
	refs, err := s.InvoiceDocuments.PendingInvoiceDocuments(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, ref := range refs {
		_, r, _, err := s.InvoicePDF(ctx, ref.OrganizationID, ref.InvoiceID)
		if err == nil {
			r.Close()
			err = s.InvoiceDocuments.ArchiveInvoiceDocument(ctx, ref.InvoiceID)
		}
		if err != nil {
			s.log.WarnContext(ctx, "bank invoice PDF archival failed", KeyInvoiceID, ref.InvoiceID)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
