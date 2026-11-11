-- +goose Up
-- Trusted control data. No default application privileges, including privileges
-- inherited from the application's default grants on the public schema.
CREATE TABLE rw_check_account (
    id text PRIMARY KEY,
    company_inn text NOT NULL UNIQUE CHECK (company_inn ~ '^([0-9]{10}|[0-9]{12})$'),
    total_limit bigint NOT NULL CHECK (total_limit > 0),
    daily_limit bigint NOT NULL CHECK (daily_limit > 0 AND daily_limit <= total_limit),
    daily_operations bigint NOT NULL CHECK (daily_operations > 0),
    killed boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE rw_check_alias (
    alias text PRIMARY KEY,
    account_id text NOT NULL REFERENCES rw_check_account(id)
);
CREATE TABLE rw_check_grant (
    id uuid PRIMARY KEY,
    account_id text NOT NULL REFERENCES rw_check_account(id),
    version bigint NOT NULL CHECK (version > 0),
    purpose text NOT NULL CHECK (purpose = 'diagnostic'),
    issued_by text NOT NULL CHECK (length(issued_by) > 0),
    policy jsonb NOT NULL,
    not_before timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > not_before),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(id,account_id)
);
CREATE TABLE rw_check_run (
    id uuid PRIMARY KEY,
    account_id text NOT NULL,
    grant_id uuid NOT NULL,
    active_grant_id uuid,
    purpose text NOT NULL CHECK (purpose = 'diagnostic'),
    manifest jsonb NOT NULL,
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[a-f0-9]{64}$'),
    external_id text NOT NULL,
    task_id bigint CHECK (task_id > 0),
    nominal bigint NOT NULL CHECK (nominal > 0),
    fee_ceiling bigint NOT NULL CHECK (fee_ceiling >= 0 AND fee_ceiling <= 9223372036854775807-nominal),
    state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared','reserved','uncertain','observed','settled','released')),
    reserved_at timestamptz,
    settled_at timestamptz,
    provider_paid_at timestamptz,
    actual_nominal bigint CHECK (actual_nominal >= 0),
    actual_fee bigint CHECK (actual_fee >= 0),
    ledger_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(grant_id,account_id) REFERENCES rw_check_grant(id,account_id),
    FOREIGN KEY(active_grant_id,account_id) REFERENCES rw_check_grant(id,account_id),
    UNIQUE(account_id,external_id),
    UNIQUE(account_id,task_id),
    CHECK ((state = 'prepared') = (reserved_at IS NULL)),
    CHECK ((state = 'settled') = (settled_at IS NOT NULL)),
    CHECK (state <> 'settled' OR (actual_nominal IS NOT NULL AND actual_fee IS NOT NULL AND ledger_id IS NOT NULL))
);
CREATE TABLE rw_check_action (
    run_id uuid NOT NULL REFERENCES rw_check_run(id),
    action text NOT NULL CHECK (action IN ('create','complete','sign_act','pay','cancel')),
    state text NOT NULL CHECK (state IN ('intent','uncertain','observed','rejected')),
    request_hash text NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    observed_at timestamptz,
    PRIMARY KEY(run_id,action)
);
CREATE TABLE rw_check_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor text NOT NULL,
    operation text NOT NULL,
    grant_id uuid,
    run_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX rw_check_account_runs ON rw_check_run(account_id,reserved_at,settled_at);
-- Minimal ownership projection; the dispatcher receives only SELECT on this view.
CREATE VIEW rw_check_ownership AS SELECT account_id, external_id, task_id, id AS run_id FROM rw_check_run;

-- +goose StatementBegin
CREATE FUNCTION rw_check_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'rw_check_grant' THEN
        IF (to_jsonb(NEW)-'revoked_at') IS DISTINCT FROM (to_jsonb(OLD)-'revoked_at') OR
           (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
            RAISE EXCEPTION 'immutable diagnostic grant' USING ERRCODE='23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'rw_check_run' THEN
        IF (NEW.id,NEW.account_id,NEW.grant_id,NEW.purpose,NEW.manifest,NEW.manifest_hash,NEW.external_id,NEW.nominal,NEW.fee_ceiling,NEW.created_at)
           IS DISTINCT FROM
           (OLD.id,OLD.account_id,OLD.grant_id,OLD.purpose,OLD.manifest,OLD.manifest_hash,OLD.external_id,OLD.nominal,OLD.fee_ceiling,OLD.created_at) OR
           (OLD.task_id IS NOT NULL AND NEW.task_id IS DISTINCT FROM OLD.task_id) OR
           (OLD.reserved_at IS NOT NULL AND NEW.reserved_at IS DISTINCT FROM OLD.reserved_at) OR
           (OLD.state IN ('settled','released') AND (to_jsonb(NEW)-'provider_paid_at') IS DISTINCT FROM (to_jsonb(OLD)-'provider_paid_at')) OR
           (OLD.provider_paid_at IS NOT NULL AND NEW.provider_paid_at IS DISTINCT FROM OLD.provider_paid_at) OR
           (OLD.state <> 'prepared' AND NEW.state = 'prepared') OR
           (OLD.state = 'uncertain' AND NEW.state = 'reserved') THEN
            RAISE EXCEPTION 'immutable diagnostic run' USING ERRCODE='23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'rw_check_action' THEN
        IF (NEW.run_id,NEW.action,NEW.request_hash,NEW.created_at) IS DISTINCT FROM (OLD.run_id,OLD.action,OLD.request_hash,OLD.created_at) OR
           NEW.state = 'intent' OR (OLD.state IN ('observed','rejected') AND NEW IS DISTINCT FROM OLD) THEN
            RAISE EXCEPTION 'immutable diagnostic action' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rw_check_grant_guard BEFORE UPDATE ON rw_check_grant FOR EACH ROW EXECUTE FUNCTION rw_check_immutable();
CREATE TRIGGER rw_check_run_guard BEFORE UPDATE ON rw_check_run FOR EACH ROW EXECUTE FUNCTION rw_check_immutable();
CREATE TRIGGER rw_check_action_guard BEFORE UPDATE ON rw_check_action FOR EACH ROW EXECUTE FUNCTION rw_check_immutable();

-- +goose StatementBegin
CREATE FUNCTION rw_check_prepare(p_id uuid,p_grant uuid,p_hash text,p_manifest jsonb,p_external text,p_nominal bigint,p_fee bigint)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE g public.rw_check_grant; existing public.rw_check_run; imported_task bigint;
BEGIN
    SELECT * INTO STRICT g FROM public.rw_check_grant WHERE id=p_grant;
    IF g.revoked_at IS NOT NULL OR clock_timestamp() < g.not_before OR clock_timestamp() >= g.expires_at OR
       p_id::text IS DISTINCT FROM g.policy->>'RunID' OR p_hash IS DISTINCT FROM g.policy->>'ManifestHash' OR
       p_nominal IS DISTINCT FROM (g.policy->>'FixedAmount')::bigint OR p_fee IS DISTINCT FROM (g.policy->>'MaxCommission')::bigint OR
       (coalesce(p_manifest->>'legacy_import_id','')='' AND p_external <> 'rw-diagnostic:'||p_id::text) THEN
        RAISE EXCEPTION 'diagnostic preparation denied' USING ERRCODE='42501';
    END IF;
    IF coalesce(p_manifest->>'legacy_import_id','')<>'' THEN
        SELECT task_id INTO STRICT imported_task FROM public.rw_check_legacy_import
        WHERE id=(p_manifest->>'legacy_import_id')::uuid AND account_id=g.account_id AND external_id=p_external
          AND terms_hash=p_manifest->>'imported_terms_hash';
        IF p_manifest->'actions' ? 'create' THEN RAISE EXCEPTION 'import cannot recreate a task'; END IF;
    END IF;
    INSERT INTO public.rw_check_run(id,account_id,grant_id,purpose,manifest,manifest_hash,external_id,nominal,fee_ceiling,task_id)
      VALUES(p_id,g.account_id,p_grant,'diagnostic',p_manifest,p_hash,p_external,p_nominal,p_fee,imported_task)
      ON CONFLICT(id) DO NOTHING;
    SELECT * INTO STRICT existing FROM public.rw_check_run WHERE id=p_id;
    IF (existing.grant_id,existing.manifest_hash,existing.manifest,existing.external_id,existing.nominal,existing.fee_ceiling)
       IS DISTINCT FROM (p_grant,p_hash,p_manifest,p_external,p_nominal,p_fee) THEN
        RAISE EXCEPTION 'diagnostic run conflict' USING ERRCODE='23514';
    END IF;
    IF imported_task IS NOT NULL THEN
      INSERT INTO public.rw_check_action(run_id,action,state,request_hash,observed_at) VALUES(p_id,'create','observed',p_hash,clock_timestamp()) ON CONFLICT(run_id,action) DO NOTHING;
    END IF;
    INSERT INTO public.rw_check_audit(actor,operation,grant_id,run_id) VALUES(session_user,'prepare',p_grant,p_id);
    RETURN p_id;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) FROM PUBLIC;

-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); policy_role text := nullif(current_setting('maxim.rw_policy_role',true),''); runner_role text := nullif(current_setting('maxim.rw_runner_role',true),''); BEGIN
    EXECUTE format('REVOKE ALL ON rw_check_account,rw_check_alias,rw_check_grant,rw_check_run,rw_check_action,rw_check_audit FROM %I',app_role);
    REVOKE ALL ON rw_check_account,rw_check_alias,rw_check_grant,rw_check_run,rw_check_action,rw_check_audit FROM PUBLIC;
    EXECUTE format('GRANT SELECT ON rw_check_ownership TO %I',app_role);
    EXECUTE format('REVOKE INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER ON rw_check_ownership FROM %I',app_role);
    IF policy_role IS NOT NULL THEN
        EXECUTE format('GRANT SELECT,INSERT,UPDATE ON rw_check_account,rw_check_alias,rw_check_grant TO %I',policy_role);
        EXECUTE format('GRANT SELECT ON rw_check_run,rw_check_action,rw_check_audit TO %I',policy_role);
    END IF;
    IF runner_role IS NOT NULL THEN
        EXECUTE format('GRANT SELECT ON rw_check_account,rw_check_alias,rw_check_grant,rw_check_run,rw_check_action TO %I',runner_role);
        EXECUTE format('GRANT EXECUTE ON FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint) TO %I',runner_role);
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Refuse rollback of a populated control journal, rather than erase authority/history.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM rw_check_account) OR EXISTS(SELECT 1 FROM rw_check_grant) OR EXISTS(SELECT 1 FROM rw_check_run) OR EXISTS(SELECT 1 FROM rw_check_audit) THEN
        RAISE EXCEPTION 'diagnostic control history must be retained';
    END IF;
END $$;
-- +goose StatementEnd
DROP VIEW rw_check_ownership;
DROP FUNCTION rw_check_prepare(uuid,uuid,text,jsonb,text,bigint,bigint);
DROP TABLE rw_check_action,rw_check_audit,rw_check_run,rw_check_grant,rw_check_alias,rw_check_account;
DROP FUNCTION rw_check_immutable();
