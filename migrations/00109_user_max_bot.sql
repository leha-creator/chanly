-- +goose Up
CREATE TABLE user_notification_max_bot (
    user_id uuid PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    max_user_id bigint NOT NULL CHECK (max_user_id > 0),
    connected_at timestamptz NOT NULL
);
CREATE TABLE notification_max_bot_connection (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('pending','connected','expired')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX notification_max_bot_connection_user_created_idx ON notification_max_bot_connection(user_id,created_at);
-- Refresh effective delivery availability in the user's other tabs and sessions.
-- +goose StatementBegin
CREATE FUNCTION notification_max_bot_realtime_capture() RETURNS trigger LANGUAGE plpgsql SET search_path=public,pg_temp AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        PERFORM realtime_emit('settings.changed','user',OLD.user_id);
    ELSE
        PERFORM realtime_emit('settings.changed','user',NEW.user_id);
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_max_bot_change AFTER INSERT OR UPDATE OR DELETE ON user_notification_max_bot
FOR EACH ROW EXECUTE FUNCTION notification_max_bot_realtime_capture();

-- +goose Down
DROP TRIGGER notification_max_bot_change ON user_notification_max_bot;
DROP FUNCTION notification_max_bot_realtime_capture();
DROP TABLE notification_max_bot_connection;
DROP TABLE user_notification_max_bot;
