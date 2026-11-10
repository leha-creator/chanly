-- name: BindRocketworkExecutor :one
INSERT INTO rocketwork_executor_binding
(environment, account_key, organization_id, executor_id, lookup_kind, lookup_value, employment_kind, verified_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (environment,account_key,organization_id) DO UPDATE SET verified_at=EXCLUDED.verified_at
WHERE rocketwork_executor_binding.executor_id=EXCLUDED.executor_id
AND rocketwork_executor_binding.lookup_kind=EXCLUDED.lookup_kind
AND rocketwork_executor_binding.lookup_value=EXCLUDED.lookup_value
AND rocketwork_executor_binding.employment_kind=EXCLUDED.employment_kind
RETURNING *;

-- name: GetRocketworkBinding :one
SELECT * FROM rocketwork_executor_binding WHERE environment=$1 AND account_key=$2 AND organization_id=$3;

-- name: PrepareRocketworkWorkflow :one
INSERT INTO rocketwork_workflow (payout_id,environment,account_key,external_id,terms,terms_hash,phase,created_at,updated_at)
SELECT p.id,$2,$3,p.id::text,$4,$5,'prepared',$6,$6 FROM payout p
JOIN rocketwork_executor_binding b ON b.organization_id=p.blogger_organization_id
 AND b.environment=$2 AND b.account_key=$3 AND b.executor_id=sqlc.arg(executor_id)
 AND b.employment_kind=sqlc.arg(employment_kind)
WHERE p.id=$1 AND p.method='rocketwork' AND p.status='created' AND p.amount=sqlc.arg(amount)
ON CONFLICT (payout_id) DO UPDATE SET updated_at=rocketwork_workflow.updated_at
WHERE rocketwork_workflow.terms_hash=EXCLUDED.terms_hash
 AND rocketwork_workflow.environment=EXCLUDED.environment AND rocketwork_workflow.account_key=EXCLUDED.account_key
RETURNING *;

-- name: GetRocketworkWorkflow :one
SELECT * FROM rocketwork_workflow WHERE payout_id=$1;

-- name: LockRocketworkWorkflow :one
SELECT * FROM rocketwork_workflow WHERE payout_id=$1 FOR UPDATE;

-- name: UpdateRocketworkWorkflow :execrows
UPDATE rocketwork_workflow SET task_id=$2,phase=$3,updated_at=$4,documents=sqlc.arg(documents),observed_status=sqlc.arg(observed_status)
WHERE payout_id=$1 AND environment <> 'legacy';

-- name: PrepareRocketworkOperation :one
INSERT INTO rocketwork_operation (payout_id,action,state,payload_hash,correlation_id,updated_at)
SELECT w.payout_id,$2,'prepared',$3,$4,$5 FROM rocketwork_workflow w
WHERE w.payout_id=$1 AND w.environment <> 'legacy'
ON CONFLICT (payout_id,action) DO UPDATE SET updated_at=rocketwork_operation.updated_at
WHERE rocketwork_operation.payload_hash=EXCLUDED.payload_hash
RETURNING *;

-- name: GetRocketworkOperation :one
SELECT * FROM rocketwork_operation WHERE payout_id=$1 AND action=$2;

-- name: TransitionRocketworkOperation :execrows
UPDATE rocketwork_operation SET state=sqlc.arg(next_state),evidence=sqlc.arg(evidence),updated_at=sqlc.arg(updated_at)
WHERE payout_id=sqlc.arg(payout_id) AND action=sqlc.arg(action) AND state=sqlc.arg(previous_state)
AND correlation_id=sqlc.arg(correlation_id);
