package domain

import (
	"context"

	"github.com/google/uuid"
)

// InvoiceIssuer creates a bank document, never a payment or a ledger credit.
// Send must make exactly one attempt: the provider has no idempotency contract.
type InvoiceIssuer interface {
	Send(context.Context, InvoiceDoc) (BankInvoiceDocument, error)
	Download(context.Context, string) ([]byte, error)
}

type BankInvoiceDocument struct {
	ID     uuid.UUID
	PDFURL string
}

// BankInvoiceDocuments persists ownership before a non-repeatable external call.
// A pending record with no remote ID must be reconciled manually, never replayed.
type BankInvoiceDocuments interface {
	ReserveInvoiceDocument(context.Context, uuid.UUID, uuid.UUID, InvoiceDoc) error
	SaveInvoiceDocument(context.Context, uuid.UUID, BankInvoiceDocument) error
	InvoiceDocument(context.Context, uuid.UUID) (BankInvoiceDocument, error)
	PendingInvoiceDocuments(context.Context) ([]InvoiceDocumentRef, error)
	ArchiveInvoiceDocument(context.Context, uuid.UUID) error
}

type InvoiceDocumentRef struct{ InvoiceID, OrganizationID uuid.UUID }
