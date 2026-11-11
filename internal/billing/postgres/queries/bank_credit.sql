-- name: CreateBankCreditPolicy :exec
INSERT INTO bank_credit_policy(run_id,policy_sha256,account_number,organization_id,max_payment,max_total,expires_at)
VALUES(sqlc.arg(run_id),sqlc.arg(policy_sha256),sqlc.arg(account_number),sqlc.arg(organization_id),sqlc.arg(max_payment),sqlc.arg(max_total),sqlc.arg(expires_at))
ON CONFLICT DO NOTHING;

-- name: LockBankCreditPolicy :one
SELECT * FROM bank_credit_policy WHERE run_id=sqlc.arg(run_id) FOR UPDATE;

-- name: FindBankCreditSpend :one
SELECT * FROM bank_credit_spend WHERE bank_transaction_id=sqlc.arg(bank_transaction_id);

-- name: AddBankCreditSpend :exec
INSERT INTO bank_credit_spend(bank_transaction_id,run_id,amount,created_at)
VALUES(sqlc.arg(bank_transaction_id),sqlc.arg(run_id),sqlc.arg(amount),sqlc.arg(created_at));

-- name: IncreaseBankCreditSpent :exec
UPDATE bank_credit_policy SET spent=spent+sqlc.arg(amount)::bigint WHERE run_id=sqlc.arg(run_id);
