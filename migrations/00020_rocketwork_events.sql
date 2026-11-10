-- +goose Up
CREATE TABLE rocketwork_event_command (
    id uuid PRIMARY KEY,
    environment text NOT NULL,
    account_key text NOT NULL,
    action text NOT NULL CHECK (action IN ('create', 'delete')),
    payload_hash text NOT NULL CHECK (length(payload_hash) = 64),
    subscription_id bigint NOT NULL DEFAULT 0 CHECK (subscription_id >= 0),
    state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared', 'in_flight', 'confirmed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (environment, account_key, action, payload_hash)
);
CREATE TABLE rocketwork_event_cursor (
    environment text NOT NULL,
    account_key text NOT NULL,
    through_at timestamptz NOT NULL,
    PRIMARY KEY (environment, account_key)
);
CREATE UNIQUE INDEX rocketwork_event_command_active ON rocketwork_event_command(environment, account_key) WHERE state = 'in_flight';
-- +goose StatementBegin
CREATE FUNCTION guard_rocketwork_event_command() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.environment, NEW.account_key, NEW.action, NEW.payload_hash) IS DISTINCT FROM
       (OLD.id, OLD.environment, OLD.account_key, OLD.action, OLD.payload_hash) OR
       (OLD.state = 'confirmed' AND NEW IS DISTINCT FROM OLD) OR
       (OLD.state = 'in_flight' AND NEW.state = 'prepared') THEN
        RAISE EXCEPTION 'immutable Rocket Work event command' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rocketwork_event_command_guard BEFORE UPDATE ON rocketwork_event_command
FOR EACH ROW EXECUTE FUNCTION guard_rocketwork_event_command();
-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
    EXECUTE format('REVOKE DELETE, TRUNCATE ON rocketwork_event_command, rocketwork_event_cursor FROM %I',app_role);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM rocketwork_event_command) OR EXISTS (SELECT 1 FROM rocketwork_event_cursor) THEN
        RAISE EXCEPTION 'Rocket Work event history must be retained';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE rocketwork_event_cursor;
DROP TABLE rocketwork_event_command;
DROP FUNCTION guard_rocketwork_event_command();
