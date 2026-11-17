-- +goose Up
ALTER TABLE notification ADD COLUMN telegram_user_id bigint;
ALTER TABLE notification DROP CONSTRAINT notification_channel_check;
ALTER TABLE notification ADD CONSTRAINT notification_channel_check CHECK (channel IN ('max_bot','telegram_bot','email','cabinet'));
ALTER TABLE notification ADD CONSTRAINT notification_telegram_user CHECK ((channel='telegram_bot')=(telegram_user_id IS NOT NULL));
ALTER TABLE inbound_event DROP CONSTRAINT inbound_event_source_check;
ALTER TABLE inbound_event ADD CONSTRAINT inbound_event_source_check CHECK (source IN ('max','rocketwork','telegram'));
ALTER TABLE outbound_request DROP CONSTRAINT outbound_request_system_check;
ALTER TABLE outbound_request ADD CONSTRAINT outbound_request_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank','telegram'));
ALTER TABLE outbound_circuit DROP CONSTRAINT outbound_circuit_system_check;
ALTER TABLE outbound_circuit ADD CONSTRAINT outbound_circuit_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank','telegram'));
CREATE TABLE user_notification_telegram_bot (
    user_id uuid PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    telegram_user_id bigint NOT NULL CHECK (telegram_user_id > 0),
    connected_at timestamptz NOT NULL
);
CREATE TABLE notification_telegram_bot_connection (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('pending','connected','expired')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX notification_telegram_bot_connection_user_created_idx ON notification_telegram_bot_connection(user_id,created_at);
-- Refresh effective delivery availability in the user's other tabs and sessions.
-- +goose StatementBegin
CREATE FUNCTION notification_telegram_bot_realtime_capture() RETURNS trigger LANGUAGE plpgsql SET search_path=public,pg_temp AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        PERFORM realtime_emit('settings.changed','user',OLD.user_id);
    ELSE
        PERFORM realtime_emit('settings.changed','user',NEW.user_id);
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_telegram_bot_change AFTER INSERT OR UPDATE OR DELETE ON user_notification_telegram_bot
FOR EACH ROW EXECUTE FUNCTION notification_telegram_bot_realtime_capture();

-- +goose Down
DELETE FROM notification WHERE channel='telegram_bot';
ALTER TABLE notification DROP CONSTRAINT notification_telegram_user;
ALTER TABLE notification DROP COLUMN telegram_user_id;
ALTER TABLE notification DROP CONSTRAINT notification_channel_check;
ALTER TABLE notification ADD CONSTRAINT notification_channel_check CHECK (channel IN ('max_bot','email','cabinet'));
DELETE FROM inbound_event WHERE source='telegram';
ALTER TABLE inbound_event DROP CONSTRAINT inbound_event_source_check;
ALTER TABLE inbound_event ADD CONSTRAINT inbound_event_source_check CHECK (source IN ('max','rocketwork'));
DELETE FROM outbound_request WHERE system='telegram';
DELETE FROM outbound_circuit WHERE system='telegram';
ALTER TABLE outbound_request DROP CONSTRAINT outbound_request_system_check;
ALTER TABLE outbound_request ADD CONSTRAINT outbound_request_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank'));
ALTER TABLE outbound_circuit DROP CONSTRAINT outbound_circuit_system_check;
ALTER TABLE outbound_circuit ADD CONSTRAINT outbound_circuit_system_check CHECK (system IN ('max','yandex_ord','rocketwork','tbank'));
DROP TRIGGER notification_telegram_bot_change ON user_notification_telegram_bot;
DROP FUNCTION notification_telegram_bot_realtime_capture();
DROP TABLE notification_telegram_bot_connection;
DROP TABLE user_notification_telegram_bot;
