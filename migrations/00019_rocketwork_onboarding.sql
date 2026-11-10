-- +goose Up
CREATE TABLE rocketwork_onboarding (
    environment text NOT NULL CHECK (environment IN ('fake','local-emulator','sandbox','production')),
    account_key text NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization(id),
    action text NOT NULL CHECK (action IN ('invite','register','update','rebind')),
    state text NOT NULL CHECK (state IN ('prepared','in_flight','confirmed','uncertain','rejected')),
    payload jsonb NOT NULL,
    payload_hash text NOT NULL CHECK (payload_hash ~ '^[a-f0-9]{64}$'),
    correlation_id uuid NOT NULL UNIQUE,
    result text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (environment,account_key,organization_id,action)
);
CREATE UNIQUE INDEX rocketwork_onboarding_active_key
ON rocketwork_onboarding(environment,account_key,organization_id) WHERE state IN ('in_flight','uncertain');
-- +goose StatementBegin
CREATE FUNCTION protect_rocketwork_onboarding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.environment,NEW.account_key,NEW.organization_id,NEW.action,NEW.payload,NEW.payload_hash,NEW.correlation_id)
       IS DISTINCT FROM (OLD.environment,OLD.account_key,OLD.organization_id,OLD.action,OLD.payload,OLD.payload_hash,OLD.correlation_id) THEN
        RAISE EXCEPTION 'immutable RocketWork onboarding' USING ERRCODE='23514';
    END IF;
    IF NEW.state <> OLD.state AND NOT (
       (OLD.state='prepared' AND NEW.state IN ('in_flight','rejected')) OR
       (OLD.state='in_flight' AND NEW.state IN ('confirmed','uncertain','rejected')) OR
       (OLD.state='uncertain' AND NEW.state IN ('confirmed','rejected'))) THEN
        RAISE EXCEPTION 'invalid RocketWork onboarding transition' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rocketwork_onboarding_immutable BEFORE UPDATE ON rocketwork_onboarding
FOR EACH ROW EXECUTE FUNCTION protect_rocketwork_onboarding();
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
    EXECUTE format('REVOKE DELETE, TRUNCATE ON rocketwork_onboarding FROM %I',app_role);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM rocketwork_onboarding) THEN
        RAISE EXCEPTION 'RocketWork onboarding rollback requires empty table';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE rocketwork_onboarding;
DROP FUNCTION protect_rocketwork_onboarding();
