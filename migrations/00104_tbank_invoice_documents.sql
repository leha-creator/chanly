-- +goose Up
CREATE TABLE bank_invoice_document (
    invoice_id uuid PRIMARY KEY REFERENCES invoice(id),
    organization_id uuid NOT NULL REFERENCES organization(id),
    request_snapshot jsonb NOT NULL,
    remote_id uuid UNIQUE,
    pdf_url text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    resolved_without_creation_at timestamptz,
    CHECK ((remote_id IS NULL AND pdf_url = '') OR (remote_id IS NOT NULL AND pdf_url <> ''))
);
-- A lost response must not allow a second bank invoice for the same organization.
CREATE UNIQUE INDEX bank_invoice_document_pending ON bank_invoice_document(organization_id) WHERE remote_id IS NULL AND resolved_without_creation_at IS NULL;

-- +goose Down
DROP TABLE bank_invoice_document;
