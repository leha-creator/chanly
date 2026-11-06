-- Separate document and creative-link receipts; preserve ambiguous legacy sends.
-- +goose Up
ALTER TABLE ord_statistics ADD COLUMN report_finance jsonb NOT NULL DEFAULT '{}';
ALTER TABLE ord_invoice ADD COLUMN report_finance jsonb NOT NULL DEFAULT '{}';
ALTER TABLE ord_invoice_item ADD COLUMN tax_snapshot jsonb NOT NULL DEFAULT '{}';
ALTER TABLE ord_statistics ADD COLUMN submission_uncertain boolean NOT NULL DEFAULT false;
ALTER TABLE ord_invoice ADD COLUMN submission_uncertain boolean NOT NULL DEFAULT false;

CREATE TABLE ord_invoice_creatives (
    ord_invoice_id uuid PRIMARY KEY REFERENCES ord_invoice(id),
    ord_external_id text,
    sync_status text NOT NULL CHECK (sync_status IN ('pending','sent','synced','error')),
    sync_request_id text,
    sync_error text,
    sync_attempts integer NOT NULL DEFAULT 0 CHECK (sync_attempts >= 0),
    synced_at timestamptz,
    payload_hash text,
    ord_status text,
    sent_at timestamptz,
    submission_uncertain boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (sync_status NOT IN ('sent','synced') OR sent_at IS NOT NULL)
);
CREATE INDEX ord_invoice_creatives_status_idx ON ord_invoice_creatives(sync_status);

-- The old bridge returned only the second receipt. Never reinterpret it as
-- document confirmation, and never automatically re-POST a legacy document.
INSERT INTO ord_invoice_creatives
    (ord_invoice_id, ord_external_id, sync_status, sync_request_id, sync_error,
     sync_attempts, synced_at, payload_hash, ord_status, sent_at, created_at, updated_at)
SELECT id, ord_external_id, sync_status, sync_request_id, sync_error,
       sync_attempts, synced_at, payload_hash, ord_status, sent_at, created_at, updated_at
FROM ord_invoice;
UPDATE ord_invoice SET sync_status='error', sync_error='legacy_invoice_requires_reconciliation',
    submission_uncertain=true, sync_request_id=NULL, synced_at=NULL
WHERE ord_external_id IS NOT NULL OR sync_request_id IS NOT NULL OR sync_attempts > 0;

-- +goose StatementBegin
DO $$
DECLARE app_role text := current_setting('maxim.app_role');
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ord_invoice_creatives TO %I', app_role);
    EXECUTE format('REVOKE TRUNCATE ON ord_invoice_creatives FROM %I', app_role);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Do not erase a known uncertain outcome by rollback.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM ord_invoice_creatives WHERE sync_request_id IS NOT NULL OR submission_uncertain)
       OR EXISTS (SELECT 1 FROM ord_invoice WHERE submission_uncertain)
       OR EXISTS (SELECT 1 FROM ord_statistics WHERE submission_uncertain) THEN
        RAISE EXCEPTION 'ORD delivery data requires reconciliation before rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE ord_invoice_creatives;
ALTER TABLE ord_invoice_item DROP COLUMN tax_snapshot;
ALTER TABLE ord_invoice DROP COLUMN report_finance;
ALTER TABLE ord_statistics DROP COLUMN report_finance;
ALTER TABLE ord_invoice DROP COLUMN submission_uncertain;
ALTER TABLE ord_statistics DROP COLUMN submission_uncertain;
