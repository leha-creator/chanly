-- name: GetRun :one
SELECT * FROM rw_check_run WHERE id = $1;

-- name: GetGrant :one
SELECT * FROM rw_check_grant WHERE id = $1;

-- name: GetCheckpoint :one
SELECT * FROM rw_check_action WHERE run_id = $1 AND action = $2;

-- name: GetOwnership :one
SELECT * FROM rw_check_ownership WHERE account_id = $1 AND task_id = $2;

-- name: GetAccount :one
SELECT * FROM rw_check_account WHERE id = $1;

-- name: PrepareRun :one
SELECT rw_check_prepare(sqlc.arg(id)::uuid,sqlc.arg(grant_id)::uuid,sqlc.arg(manifest_hash)::text,sqlc.arg(manifest)::jsonb,sqlc.arg(external_id)::text,sqlc.arg(nominal)::bigint,sqlc.arg(fee_ceiling)::bigint);

-- name: CommitIntent :one
SELECT rw_check_intent(sqlc.arg(run_id)::uuid,sqlc.arg(action)::text,sqlc.arg(request_hash)::text)::boolean;

-- name: ObserveAction :exec
SELECT rw_check_observe(sqlc.arg(run_id)::uuid,sqlc.arg(action)::text,sqlc.arg(state)::text,sqlc.narg(task_id)::bigint);

-- name: ControlTime :one
SELECT clock_timestamp()::timestamptz;

-- name: SettleRun :exec
SELECT rw_check_settle(sqlc.arg(run_id)::uuid,sqlc.arg(nominal)::bigint,sqlc.arg(fee)::bigint,sqlc.arg(ledger_id)::uuid);

-- name: IssueGrant :one
SELECT rw_check_issue(sqlc.arg(id)::uuid,sqlc.arg(policy)::jsonb);

-- name: RevokeGrant :exec
SELECT rw_check_revoke(sqlc.arg(id)::uuid);

-- name: KillAccount :exec
SELECT rw_check_kill(sqlc.arg(account_id)::text,sqlc.arg(killed)::boolean);

-- name: ConfigureAccount :exec
INSERT INTO rw_check_account(id,company_inn,total_limit,daily_limit,daily_operations)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT(id) DO UPDATE SET total_limit=excluded.total_limit,daily_limit=excluded.daily_limit,daily_operations=excluded.daily_operations
WHERE rw_check_account.company_inn=excluded.company_inn;

-- name: BindAccountAlias :exec
INSERT INTO rw_check_alias(alias,account_id) VALUES($1,$2) ON CONFLICT(alias) DO NOTHING;

-- name: ResolveAlias :one
SELECT account_id FROM rw_check_alias WHERE alias=$1;

-- name: ControlPrivileges :one
SELECT current_user::text AS actor,has_table_privilege(current_user,'rw_check_grant','INSERT')::boolean AS policy_owner,
 (current_setting('is_superuser')='on')::boolean AS superuser;

-- name: DiagnosticEventOwner :one
SELECT run_id FROM rw_check_dispatch_ownership
WHERE alias=sqlc.arg(alias)::text AND external_id=sqlc.arg(external_id)::text
 AND (task_id IS NULL OR task_id=sqlc.arg(task_id)::bigint);

-- name: DiagnosticBudgetStatus :one
WITH facts AS (
 SELECT coalesce(sum(actual_nominal::numeric+actual_fee) FILTER(WHERE state='settled'),0) AS spent,
 coalesce(sum(actual_nominal::numeric+actual_fee) FILTER(WHERE state='settled' AND settled_at >= date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0) AS daily_spent,
 coalesce(sum(nominal::numeric+fee_ceiling) FILTER(WHERE reserved_at IS NOT NULL AND state NOT IN ('settled','released')),0) AS held,
 count(*) FILTER(WHERE reserved_at >= date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS operations
 FROM rw_check_run WHERE account_id=sqlc.arg(account_id)::text
)
SELECT a.total_limit::text AS total_limit,a.daily_limit::text AS daily_limit,a.daily_operations,a.killed,f.spent::text AS spent,f.daily_spent::text AS daily_spent,f.held::text AS held,f.operations,
 (a.total_limit::numeric-f.spent-f.held)::text AS remaining,
 (a.daily_limit::numeric-f.daily_spent-f.held)::text AS daily_remaining
FROM rw_check_account a CROSS JOIN facts f WHERE a.id=sqlc.arg(account_id)::text;

-- name: ImportLegacy :exec
SELECT rw_check_import(sqlc.arg(id)::uuid,sqlc.arg(account_id)::text,sqlc.arg(external_id)::text,sqlc.arg(task_id)::bigint,sqlc.arg(manifest_hash)::text,sqlc.arg(terms_hash)::text,sqlc.arg(status)::text);

-- name: FreezeRun :exec
SELECT rw_check_freeze(sqlc.arg(id)::uuid);

-- name: ReleaseRun :exec
SELECT rw_check_release(sqlc.arg(id)::uuid);

-- name: ReauthorizeRun :exec
SELECT rw_check_reauthorize(sqlc.arg(run_id)::uuid,sqlc.arg(grant_id)::uuid);

-- name: RecordProviderPaidAt :exec
SELECT rw_check_paid_at(sqlc.arg(run_id)::uuid,sqlc.arg(paid_at)::timestamptz);
