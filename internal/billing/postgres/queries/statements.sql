-- Выписки и поступления (S-30, R-12, R-13).

-- name: InsertBankStatement :exec
INSERT INTO bank_statement (id, uploaded_by, storage_key, file_sha256, account_number, date_from, date_to,
    opening_balance, closing_balance, incoming_count, matched_count, unmatched_count, duplicate_count, source, complete, created_at, updated_at)
VALUES (@id, @uploaded_by, @storage_key, @file_sha256, @account_number, @date_from, @date_to,
    @opening_balance, @closing_balance, @incoming_count, @matched_count, @unmatched_count, @duplicate_count, @source, @complete, @created_at, @created_at);

-- name: BankStatementExists :one
SELECT EXISTS (SELECT 1 FROM bank_statement WHERE file_sha256 = @file_sha256) AS found;

-- name: UpdateBankStatementCounts :exec
UPDATE bank_statement SET incoming_count = @incoming_count,
    matched_count = (SELECT count(*) FROM bank_transaction bt WHERE bt.bank_statement_id = @id AND bt.match_status = 'matched'),
    unmatched_count = (SELECT count(*) FROM bank_transaction bt WHERE bt.bank_statement_id = @id AND bt.match_status = 'unmatched'),
    duplicate_count = @duplicate_count, updated_at = @updated_at,
    opening_balance = @opening_balance, closing_balance = @closing_balance
WHERE id = @id;

-- name: LatestBankStatement :one
SELECT * FROM bank_statement ORDER BY date_to DESC NULLS LAST, created_at DESC, id DESC LIMIT 1;

-- name: InsertBankTransaction :execrows
INSERT INTO bank_transaction (id, bank_statement_id, external_id, operation_date, amount, payer_inn, payer_name,
    payer_account, purpose, match_status, invoice_id, organization_id, matched_by, ignored_reason, created_at, updated_at)
VALUES (@id, @bank_statement_id, @external_id, @operation_date, @amount, @payer_inn, @payer_name,
    @payer_account, @purpose, @match_status, @invoice_id, @organization_id, @matched_by, @ignored_reason, @created_at, @created_at)
ON CONFLICT ON CONSTRAINT bank_transaction_external_id_key DO NOTHING;

-- name: GetBankTransactionForUpdate :one
SELECT * FROM bank_transaction WHERE id = @id FOR UPDATE;

-- name: UpdateBankTransaction :exec
UPDATE bank_transaction SET match_status = @match_status, invoice_id = @invoice_id, organization_id = @organization_id,
    matched_by = @matched_by, ignored_reason = @ignored_reason, updated_at = @updated_at
WHERE id = @id;

-- name: ListBankTransactions :many
SELECT * FROM bank_transaction
WHERE (@match_status::text = '' OR match_status = @match_status)
  AND (NOT @has_cursor::bool OR (created_at, id) < (@cursor_at::timestamptz, @cursor_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @lim;

-- name: UnmatchedBankTotal :one
SELECT count(*)::int AS n, coalesce(sum(amount), 0)::bigint AS total FROM bank_transaction WHERE match_status = 'unmatched';

-- name: CountBankTransactionsByStatus :many
SELECT match_status, count(*)::int AS n FROM bank_transaction GROUP BY match_status ORDER BY match_status;

-- name: AdminListBankTransactions :many
-- Поступления для администратора (adm/payments), новые сверху; смещение и общее число.
SELECT * FROM bank_transaction
WHERE (@match_status::text = '' OR match_status = @match_status)
ORDER BY created_at DESC, id DESC
LIMIT @lim OFFSET @off;

-- name: AdminCountBankTransactions :one
SELECT count(*)::int FROM bank_transaction WHERE (@match_status::text = '' OR match_status = @match_status);

-- name: GetBankStatement :one
SELECT * FROM bank_statement WHERE id = @id;

-- name: LatestUploadedBankStatement :one
-- Последняя загруженная выписка (итоги загрузки adm/payments).
SELECT * FROM bank_statement ORDER BY created_at DESC, id DESC LIMIT 1;
