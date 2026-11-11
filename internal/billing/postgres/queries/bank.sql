-- name: LockBankIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended('bank-identity:' || sqlc.arg(account_number)::text,0));

-- name: FindBankAliases :many
SELECT external_id, fingerprint FROM bank_operation_identity
WHERE account_number=sqlc.arg(account_number) AND alias=ANY(sqlc.arg(aliases)::text[]);

-- name: SaveBankAlias :exec
INSERT INTO bank_operation_identity(account_number,alias,external_id,fingerprint)
VALUES (sqlc.arg(account_number),sqlc.arg(alias),sqlc.arg(external_id),sqlc.arg(fingerprint)) ON CONFLICT DO NOTHING;

-- name: LegacyBankIncoming :many
SELECT t.* FROM bank_transaction t JOIN bank_statement s ON s.id=t.bank_statement_id
WHERE s.account_number=sqlc.arg(account_number) AND t.external_id=ANY(sqlc.arg(external_ids)::text[]);

-- name: ObserveBankIncoming :exec
INSERT INTO bank_operation_observation(id,account_number,operation_id,external_id,document_number,doc_date,charge_date,operation_date,conflict,statement_id,evidence)
VALUES (sqlc.arg(id),sqlc.arg(account_number),sqlc.arg(operation_id),sqlc.arg(external_id),sqlc.arg(document_number),sqlc.arg(doc_date),sqlc.arg(charge_date),sqlc.arg(operation_date),sqlc.arg(conflict),sqlc.arg(statement_id),sqlc.arg(evidence));

-- name: MissingBankOperations :one
SELECT count(DISTINCT old.external_id)::int FROM bank_operation_observation old
WHERE old.account_number=sqlc.arg(account_number) AND old.operation_id<>''
 AND old.operation_date>=sqlc.arg(period_from)::timestamptz AND old.operation_date<sqlc.arg(period_to)::timestamptz
 AND old.statement_id<>sqlc.arg(statement_id) AND NOT old.conflict
 AND NOT EXISTS (SELECT 1 FROM bank_operation_observation current
 WHERE current.account_number=old.account_number AND current.external_id=old.external_id AND current.statement_id=sqlc.arg(statement_id));

-- name: LoadBankProgress :one
SELECT progress FROM bank_sync_progress WHERE account_number=sqlc.arg(account_number);

-- name: SaveBankProgress :exec
INSERT INTO bank_sync_progress(account_number,progress) VALUES (sqlc.arg(account_number),sqlc.arg(progress))
ON CONFLICT(account_number) DO UPDATE SET progress=excluded.progress,updated_at=now();

-- name: BankStatementByHash :one
SELECT * FROM bank_statement WHERE file_sha256=sqlc.arg(file_sha256);

-- name: CompleteBankStatement :exec
UPDATE bank_statement SET complete=true WHERE id=sqlc.arg(id);
