-- name: PrepareWithdrawal :one
INSERT INTO rocketwork_withdrawal (id,environment,account_key,terms,terms_hash)
VALUES (@id,@environment,@account_key,@terms,@terms_hash)
ON CONFLICT (id) DO UPDATE SET updated_at=rocketwork_withdrawal.updated_at RETURNING *;

-- name: GetWithdrawal :one
SELECT * FROM rocketwork_withdrawal WHERE id = @id;

-- name: LockWithdrawal :one
SELECT * FROM rocketwork_withdrawal WHERE id = @id FOR UPDATE;

-- name: ClaimWithdrawal :execrows
UPDATE rocketwork_withdrawal SET state='in_flight',updated_at=now() WHERE id = @id AND state='prepared';

-- name: AttachWithdrawal :execrows
UPDATE rocketwork_withdrawal SET task_id = @task_id,state='observed',updated_at=now() WHERE id = @id AND state IN ('in_flight','observed');

-- name: SettleWithdrawal :execrows
UPDATE rocketwork_withdrawal SET state='settled',updated_at=now() WHERE id = @id AND state='observed';
