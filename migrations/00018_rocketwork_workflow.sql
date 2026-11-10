-- +goose Up
ALTER TABLE payout ADD COLUMN reconciliation_required boolean NOT NULL DEFAULT false;
CREATE TABLE rocketwork_executor_binding (
    environment text NOT NULL CHECK (environment IN ('fake','local-emulator','sandbox','production')),
    account_key text NOT NULL CHECK (length(account_key) BETWEEN 1 AND 200),
    organization_id uuid NOT NULL REFERENCES organization(id),
    executor_id bigint NOT NULL CHECK (executor_id > 0),
    lookup_kind text NOT NULL CHECK (lookup_kind IN ('inn','phone','email')),
    lookup_value text NOT NULL CHECK (length(lookup_value) BETWEEN 1 AND 320),
    employment_kind text NOT NULL CHECK (employment_kind IN ('selfemployed','entrepreneur')),
    verified_at timestamptz NOT NULL,
    PRIMARY KEY (environment, account_key, organization_id),
    UNIQUE (environment, account_key, executor_id),
    UNIQUE (environment, account_key, lookup_kind, lookup_value)
);

CREATE TABLE rocketwork_workflow (
    payout_id uuid PRIMARY KEY REFERENCES payout(id),
    environment text NOT NULL CHECK (environment IN ('legacy','fake','local-emulator','sandbox','production')),
    account_key text NOT NULL CHECK (length(account_key) BETWEEN 1 AND 200),
    external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 200),
    task_id bigint CHECK (task_id > 0),
	observed_status text NOT NULL DEFAULT '',
    terms jsonb NOT NULL,
    terms_hash text NOT NULL,
    documents jsonb NOT NULL DEFAULT '{}'::jsonb,
    phase text NOT NULL CHECK (phase IN ('legacy','prepared','agreement','acceptance','payment','documents','complete','canceled','cancel_requested','reconcile')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (environment,account_key,external_id),
    UNIQUE (environment,account_key,task_id),
    CHECK ((environment = 'legacy' AND phase = 'legacy' AND task_id IS NULL) OR
           (environment <> 'legacy' AND terms_hash ~ '^[a-f0-9]{64}$'))
);

-- Old c-*/p-* identifiers and historical live attempts are not verified wire
-- identities. Keep the source rows intact; explicit reconciliation is required.
INSERT INTO rocketwork_workflow (payout_id,environment,account_key,external_id,terms,terms_hash,phase,created_at,updated_at)
SELECT id,'legacy','unverified',id::text,'{}'::jsonb,'','legacy',created_at,updated_at
FROM payout WHERE method = 'rocketwork';
UPDATE payout SET reconciliation_required=true WHERE method='rocketwork';

CREATE TABLE rocketwork_operation (
    payout_id uuid NOT NULL REFERENCES rocketwork_workflow(payout_id),
    action text NOT NULL CHECK (action IN ('create','complete','sign_act','pay','cancel','withdrawal')),
    state text NOT NULL CHECK (state IN ('prepared','in_flight','confirmed','uncertain','rejected')),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^[a-f0-9]{64}$'),
    correlation_id uuid NOT NULL UNIQUE,
    evidence text NOT NULL DEFAULT '' CHECK (length(evidence)<=200),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (payout_id,action)
);
CREATE UNIQUE INDEX rocketwork_operation_active_key ON rocketwork_operation(payout_id)
WHERE state IN ('in_flight','uncertain');

-- +goose StatementBegin
CREATE FUNCTION protect_rocketwork_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.payout_id,NEW.action,NEW.payload_hash,NEW.correlation_id)
       IS DISTINCT FROM (OLD.payout_id,OLD.action,OLD.payload_hash,OLD.correlation_id) THEN
        RAISE EXCEPTION 'immutable RocketWork operation' USING ERRCODE='23514';
    END IF;
    IF NEW.state <> OLD.state AND NOT (
       (OLD.state='prepared' AND NEW.state IN ('in_flight','rejected')) OR
       (OLD.state='in_flight' AND NEW.state IN ('confirmed','uncertain','rejected')) OR
       (OLD.state='uncertain' AND NEW.state IN ('confirmed','rejected'))) THEN
        RAISE EXCEPTION 'invalid RocketWork operation transition' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rocketwork_operation_immutable BEFORE UPDATE ON rocketwork_operation
FOR EACH ROW EXECUTE FUNCTION protect_rocketwork_operation();
-- +goose StatementEnd

-- Existing payout status remains created/sent while the workflow waits; hence
-- payout_placement_live_key still protects every new nonterminal workflow.
-- Also protect legacy/uncertain operations if old code attempts failed -> retry.
-- +goose StatementBegin
CREATE FUNCTION protect_rocketwork_workflow() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.payout_id,NEW.environment,NEW.account_key,NEW.external_id,NEW.terms,NEW.terms_hash)
           IS DISTINCT FROM (OLD.payout_id,OLD.environment,OLD.account_key,OLD.external_id,OLD.terms,OLD.terms_hash)
           OR (OLD.task_id IS NOT NULL AND NEW.task_id IS DISTINCT FROM OLD.task_id) THEN
            RAISE EXCEPTION 'immutable RocketWork workflow' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rocketwork_workflow_immutable BEFORE UPDATE ON rocketwork_workflow
FOR EACH ROW EXECUTE FUNCTION protect_rocketwork_workflow();
CREATE FUNCTION sync_rocketwork_retry_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE payout SET reconciliation_required=(NEW.phase <> 'canceled') WHERE id=NEW.payout_id;
    RETURN NEW;
END $$;
CREATE TRIGGER rocketwork_workflow_retry_guard AFTER INSERT OR UPDATE ON rocketwork_workflow
FOR EACH ROW EXECUTE FUNCTION sync_rocketwork_retry_guard();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION guard_rocketwork_retry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Serialize inserts even if the old payout has status failed.
    PERFORM 1 FROM placement WHERE id=NEW.placement_id FOR UPDATE;
    IF EXISTS (SELECT 1 FROM payout p JOIN rocketwork_workflow w ON w.payout_id=p.id
               WHERE p.placement_id=NEW.placement_id AND p.id<>NEW.id
               AND w.phase NOT IN ('canceled')) THEN
        RAISE EXCEPTION 'RocketWork reconciliation required' USING ERRCODE='23505', CONSTRAINT='payout_placement_live_key';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rocketwork_retry_guard BEFORE INSERT ON payout
FOR EACH ROW EXECUTE FUNCTION guard_rocketwork_retry();
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
    EXECUTE format('REVOKE DELETE, TRUNCATE ON rocketwork_executor_binding, rocketwork_workflow, rocketwork_operation FROM %I',app_role);
END $$;
-- +goose StatementEnd

-- +goose Down
-- Refuse to discard financial checkpoints, including legacy unresolved rows.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM rocketwork_workflow) OR EXISTS (SELECT 1 FROM rocketwork_executor_binding) THEN
        RAISE EXCEPTION 'RocketWork workflow rollback requires empty tables; preserve financial history';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER rocketwork_retry_guard ON payout;
DROP FUNCTION guard_rocketwork_retry();
DROP TABLE rocketwork_operation;
DROP FUNCTION protect_rocketwork_operation();
DROP TABLE rocketwork_workflow;
DROP FUNCTION protect_rocketwork_workflow();
DROP FUNCTION sync_rocketwork_retry_guard();
DROP TABLE rocketwork_executor_binding;
ALTER TABLE payout DROP COLUMN reconciliation_required;
