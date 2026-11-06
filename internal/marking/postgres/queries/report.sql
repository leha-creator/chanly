-- Ежемесячная отчётность ОРД (этап «API администратора и отчётность ОРД», R-18 … R-24).

-- name: InsertViewsSnapshot :execrows
INSERT INTO publication_views_snapshot (publication_id, period, views, views_checked_at, created_at)
VALUES (@publication_id, @period, @views, @views_checked_at, @created_at)
ON CONFLICT (publication_id, period) DO NOTHING;

-- name: ListViewsSnapshots :many
SELECT publication_id, views FROM publication_views_snapshot WHERE period = @period;

-- name: ListOrdStatistics :many
SELECT * FROM ord_statistics WHERE period = @period ORDER BY created_at, id;

-- name: GetOrdStatisticsForUpdate :one
SELECT * FROM ord_statistics WHERE id = @id FOR UPDATE;

-- name: InsertOrdStatistics :exec
INSERT INTO ord_statistics (
    id, creative_id, ord_platform_id, placement_id, period, impressions, shown_from, shown_to, amount,
    ord_external_id, sync_status, sync_request_id, sync_error, sync_attempts, synced_at, payload_hash, ord_status,
    sent_at, created_at, updated_at, submission_uncertain, report_finance
) VALUES (
    @id, @creative_id, @ord_platform_id, @placement_id, @period, @impressions, @shown_from, @shown_to, @amount,
    @ord_external_id, @sync_status, @sync_request_id, @sync_error, @sync_attempts, @synced_at, @payload_hash, @ord_status,
    @sent_at, @created_at, @updated_at, @submission_uncertain, @report_finance
);

-- name: SaveOrdStatistics :exec
UPDATE ord_statistics SET
    impressions = @impressions, shown_from = @shown_from, shown_to = @shown_to, amount = @amount,
    ord_external_id = @ord_external_id, sync_status = @sync_status, sync_request_id = @sync_request_id,
    sync_error = @sync_error, sync_attempts = @sync_attempts, synced_at = @synced_at, payload_hash = @payload_hash,
    ord_status = @ord_status, sent_at = @sent_at, updated_at = @updated_at, submission_uncertain = @submission_uncertain, report_finance = @report_finance
WHERE id = @id;

-- name: DeleteOrdStatistics :exec
DELETE FROM ord_statistics WHERE id = @id;

-- name: ListOrdInvoices :many
SELECT * FROM ord_invoice WHERE period = @period ORDER BY number;

-- name: ListOrdInvoiceItems :many
SELECT i.* FROM ord_invoice_item i JOIN ord_invoice v ON v.id = i.ord_invoice_id
WHERE v.period = @period ORDER BY i.ord_invoice_id, i.creative_id, i.ord_platform_id;

-- name: ListOrdInvoiceItemsOf :many
SELECT * FROM ord_invoice_item WHERE ord_invoice_id = @ord_invoice_id ORDER BY creative_id, ord_platform_id;

-- name: GetOrdInvoiceForUpdate :one
SELECT * FROM ord_invoice WHERE id = @id FOR UPDATE;

-- name: InsertOrdInvoice :exec
INSERT INTO ord_invoice (
    id, ord_contract_id, period, type, number, invoice_date, amount, ord_external_id, sync_status, sync_request_id,
    sync_error, sync_attempts, synced_at, payload_hash, ord_status, sent_at, created_at, updated_at, submission_uncertain, report_finance
) VALUES (
    @id, @ord_contract_id, @period, @type, @number, @invoice_date, @amount, @ord_external_id, @sync_status, @sync_request_id,
    @sync_error, @sync_attempts, @synced_at, @payload_hash, @ord_status, @sent_at, @created_at, @updated_at, @submission_uncertain, @report_finance
);

-- name: SaveOrdInvoice :exec
UPDATE ord_invoice SET
    type = @type, invoice_date = @invoice_date, amount = @amount, ord_external_id = @ord_external_id,
    sync_status = @sync_status, sync_request_id = @sync_request_id, sync_error = @sync_error,
    sync_attempts = @sync_attempts, synced_at = @synced_at, payload_hash = @payload_hash, ord_status = @ord_status,
    sent_at = @sent_at, updated_at = @updated_at, submission_uncertain = @submission_uncertain, report_finance = @report_finance
WHERE id = @id;

-- name: DeleteOrdInvoiceItems :exec
DELETE FROM ord_invoice_item WHERE ord_invoice_id = @ord_invoice_id;

-- name: InsertOrdInvoiceItem :exec
INSERT INTO ord_invoice_item (ord_invoice_id, creative_id, ord_platform_id, amount, tax_snapshot)
VALUES (@ord_invoice_id, @creative_id, @ord_platform_id, @amount, @tax_snapshot);

-- name: DeleteOrdInvoice :exec
DELETE FROM ord_invoice WHERE id = @id;

-- name: GetOrdReport :one
SELECT * FROM ord_report WHERE period = @period;

-- name: UpsertOrdReport :exec
INSERT INTO ord_report (period, built_at, built_by, send_requested_at, send_requested_by, updated_at)
VALUES (@period, @built_at, @built_by, @send_requested_at, @send_requested_by, @updated_at)
ON CONFLICT (period) DO UPDATE SET
    built_at = EXCLUDED.built_at, built_by = EXCLUDED.built_by, send_requested_at = EXCLUDED.send_requested_at,
    send_requested_by = EXCLUDED.send_requested_by, updated_at = EXCLUDED.updated_at;

-- name: ListOrdReports :many
SELECT * FROM ord_report ORDER BY period DESC LIMIT 120;

-- name: GetOrdInvoiceLinks :one
SELECT * FROM ord_invoice_creatives WHERE ord_invoice_id = @ord_invoice_id;

-- name: SaveOrdInvoiceLinks :exec
INSERT INTO ord_invoice_creatives (
 ord_invoice_id, ord_external_id, sync_status, sync_request_id, sync_error, sync_attempts,
 synced_at, payload_hash, ord_status, sent_at, submission_uncertain, created_at, updated_at
) VALUES (
 @ord_invoice_id, @ord_external_id, @sync_status, @sync_request_id, @sync_error, @sync_attempts,
 @synced_at, @payload_hash, @ord_status, @sent_at, @submission_uncertain, @created_at, @updated_at
) ON CONFLICT (ord_invoice_id) DO UPDATE SET
 ord_external_id=EXCLUDED.ord_external_id, sync_status=EXCLUDED.sync_status,
 sync_request_id=EXCLUDED.sync_request_id, sync_error=EXCLUDED.sync_error,
 sync_attempts=EXCLUDED.sync_attempts, synced_at=EXCLUDED.synced_at,
 payload_hash=EXCLUDED.payload_hash, ord_status=EXCLUDED.ord_status,
 sent_at=EXCLUDED.sent_at, submission_uncertain=EXCLUDED.submission_uncertain, updated_at=EXCLUDED.updated_at;

-- name: DeleteOrdInvoiceLinks :exec
DELETE FROM ord_invoice_creatives WHERE ord_invoice_id = @ord_invoice_id;
