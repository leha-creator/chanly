-- name: ReserveInvoiceDocument :exec
INSERT INTO bank_invoice_document(invoice_id, organization_id, request_snapshot) VALUES (@invoice_id, @organization_id, @request_snapshot);

-- name: SaveInvoiceDocument :execrows
UPDATE bank_invoice_document SET remote_id = @remote_id, pdf_url = @pdf_url
WHERE invoice_id = @invoice_id AND remote_id IS NULL;

-- name: GetInvoiceDocument :one
SELECT remote_id, pdf_url FROM bank_invoice_document WHERE invoice_id = @invoice_id;

-- name: PendingInvoiceDocuments :many
SELECT invoice_id, organization_id FROM bank_invoice_document
WHERE remote_id IS NOT NULL AND archived_at IS NULL ORDER BY created_at LIMIT 50;

-- name: ArchiveInvoiceDocument :exec
UPDATE bank_invoice_document SET archived_at = now() WHERE invoice_id = @invoice_id AND remote_id IS NOT NULL;
