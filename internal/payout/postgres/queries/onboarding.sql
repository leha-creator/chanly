-- name: PrepareRocketworkOnboarding :one
INSERT INTO rocketwork_onboarding
(environment,account_key,organization_id,action,state,payload,payload_hash,correlation_id,created_at,updated_at)
VALUES ($1,$2,$3,$4,'prepared',$5,$6,$7,$8,$8)
ON CONFLICT (environment,account_key,organization_id,action) DO UPDATE SET updated_at=rocketwork_onboarding.updated_at
WHERE rocketwork_onboarding.payload_hash=EXCLUDED.payload_hash
RETURNING *;

-- name: GetRocketworkOnboarding :one
SELECT * FROM rocketwork_onboarding WHERE environment=$1 AND account_key=$2 AND organization_id=$3 AND action=$4;

-- name: TransitionRocketworkOnboarding :execrows
UPDATE rocketwork_onboarding SET state=sqlc.arg(next_state),result=sqlc.arg(result),updated_at=sqlc.arg(updated_at)
WHERE environment=sqlc.arg(environment) AND account_key=sqlc.arg(account_key) AND organization_id=sqlc.arg(organization_id)
AND action=sqlc.arg(action) AND state=sqlc.arg(previous_state) AND correlation_id=sqlc.arg(correlation_id);
