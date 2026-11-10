-- name: GetReturnEvidence :one
SELECT * FROM rocketwork_return_evidence WHERE id = @id;

-- name: ReturnedAmount :one
SELECT coalesce(sum(amount),0)::bigint FROM rocketwork_return_evidence WHERE payout_id = @payout_id;

-- name: InsertReturnEvidence :exec
INSERT INTO rocketwork_return_evidence (id,payout_id,document_id,admin_id,approver_id,amount,destination,reason,received_at)
VALUES (@id,@payout_id,@document_id,@admin_id,@approver_id,@amount,@destination,@reason,@received_at);
