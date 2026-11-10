-- Исполнители Рокет Ворк (04 §7, S-11).

-- name: ContractorIDsAfter :many
SELECT id FROM rocketwork_contractor WHERE id > @after_id ORDER BY id LIMIT 100;

-- name: InsertContractor :execrows
INSERT INTO rocketwork_contractor (id, organization_id, external_id, status, blocked_reason, onboarding_url,
    status_checked_at, last_error, created_at, updated_at)
VALUES (@id, @organization_id, @external_id, @status, @blocked_reason, @onboarding_url,
    @status_checked_at, @last_error, @created_at, @updated_at)
ON CONFLICT (organization_id) DO NOTHING;

-- name: GetContractorByOrg :one
SELECT * FROM rocketwork_contractor WHERE organization_id = @organization_id;

-- name: GetContractorByOrgForUpdate :one
SELECT * FROM rocketwork_contractor WHERE organization_id = @organization_id FOR UPDATE;

-- name: GetContractor :one
SELECT * FROM rocketwork_contractor WHERE id = @id;

-- name: GetContractorForUpdate :one
SELECT * FROM rocketwork_contractor WHERE id = @id FOR UPDATE;

-- name: GetContractorByExternalID :one
SELECT * FROM rocketwork_contractor WHERE external_id = @external_id;

-- name: GetContractorByExternalIDForUpdate :one
SELECT * FROM rocketwork_contractor WHERE external_id = @external_id FOR UPDATE;

-- name: UpdateContractor :execrows
UPDATE rocketwork_contractor SET external_id = @external_id, status = @status, blocked_reason = @blocked_reason,
    onboarding_url = @onboarding_url, status_checked_at = @status_checked_at, last_error = @last_error,
    updated_at = @updated_at
WHERE id = @id;

-- name: CountContractorsByStatus :many
SELECT status, count(*)::int AS n FROM rocketwork_contractor GROUP BY status ORDER BY status;
