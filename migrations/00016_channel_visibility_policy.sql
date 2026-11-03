-- +goose Up
-- release: contract
-- The removed predicate is already enforced by the application visibility
-- policy. Deploy after that policy; no columns or stored facts are removed.
-- Visibility is environment-dependent, like the configured RKN threshold.
-- channel/domain enforces public channels outside development; keep is_public
-- as the observed MAX fact, not an eligibility override.
ALTER TABLE channel DROP CONSTRAINT channel_active_requirements;
ALTER TABLE channel ADD CONSTRAINT channel_active_requirements CHECK (status <> 'active' OR (
    organization_id IS NOT NULL AND category_id IS NOT NULL
    AND bot_permissions @> ARRAY['write', 'delete']));
COMMENT ON CONSTRAINT channel_active_requirements ON channel IS
    'active: organization, category, write/delete rights. Price: channel_active_requires_price. Visibility and RKN threshold: channel/domain environment policy.';

-- +goose Down
-- Deliberately fails if private active channels remain: do not silently alter data.
ALTER TABLE channel DROP CONSTRAINT channel_active_requirements;
ALTER TABLE channel ADD CONSTRAINT channel_active_requirements CHECK (status <> 'active' OR (
    organization_id IS NOT NULL AND is_public AND category_id IS NOT NULL
    AND bot_permissions @> ARRAY['write', 'delete']));
COMMENT ON CONSTRAINT channel_active_requirements ON channel IS
    'active: organization, public visibility, category, write/delete rights. Price: channel_active_requires_price. RKN threshold: channel/domain.';
