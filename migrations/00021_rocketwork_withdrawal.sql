-- +goose Up
CREATE TABLE rocketwork_withdrawal (
    id uuid PRIMARY KEY,
    environment text NOT NULL,
    account_key text NOT NULL,
    terms jsonb NOT NULL,
    terms_hash text NOT NULL CHECK (terms_hash ~ '^[a-f0-9]{64}$'),
    task_id bigint CHECK (task_id > 0),
    state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared','in_flight','observed','settled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (environment,account_key,task_id)
);
-- +goose StatementBegin
CREATE FUNCTION guard_rocketwork_withdrawal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id,NEW.environment,NEW.account_key,NEW.terms,NEW.terms_hash) IS DISTINCT FROM
       (OLD.id,OLD.environment,OLD.account_key,OLD.terms,OLD.terms_hash) OR
       (OLD.task_id IS NOT NULL AND NEW.task_id IS DISTINCT FROM OLD.task_id) OR
       (OLD.state = 'settled' AND NEW.state <> 'settled') OR
       (OLD.state <> 'prepared' AND NEW.state = 'prepared') THEN
       RAISE EXCEPTION 'immutable Rocket Work withdrawal' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rocketwork_withdrawal_guard BEFORE UPDATE ON rocketwork_withdrawal FOR EACH ROW EXECUTE FUNCTION guard_rocketwork_withdrawal();
CREATE TABLE rocketwork_return_evidence (
    id uuid PRIMARY KEY,
    payout_id uuid NOT NULL REFERENCES payout(id),
    document_id uuid NOT NULL UNIQUE,
    admin_id uuid NOT NULL REFERENCES "user"(id),
    approver_id uuid NOT NULL REFERENCES "user"(id),
    amount bigint NOT NULL CHECK (amount > 0),
    destination text NOT NULL CHECK (destination IN ('bank','rocketwork_deposit')),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    received_at timestamptz NOT NULL,
    CHECK (admin_id <> approver_id)
);
-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
    EXECUTE format('REVOKE DELETE, TRUNCATE ON rocketwork_withdrawal FROM %I',app_role);
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON rocketwork_return_evidence FROM %I',app_role);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM rocketwork_withdrawal) OR EXISTS (SELECT 1 FROM rocketwork_return_evidence) THEN RAISE EXCEPTION 'withdrawal/return history must be retained'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE rocketwork_withdrawal;
DROP TABLE rocketwork_return_evidence;
DROP FUNCTION guard_rocketwork_withdrawal();
