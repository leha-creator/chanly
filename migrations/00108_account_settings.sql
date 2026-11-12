-- +goose Up
ALTER TABLE notification DROP CONSTRAINT notification_status_check;
ALTER TABLE notification ADD CONSTRAINT notification_status_check CHECK(status IN ('pending','sent','failed','read','suppressed'));
CREATE TABLE user_settings (
    user_id uuid PRIMARY KEY REFERENCES "user"(id),
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    hide_notification_preview boolean NOT NULL DEFAULT false,
    quiet_hours jsonb NOT NULL DEFAULT '{"enabled":false,"start":"22:00","end":"08:00","timezone":"Europe/Moscow"}',
    delivery jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL
);
CREATE TABLE email_change (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES "user"(id),
    current_email citext NOT NULL,
    new_email citext NOT NULL,
    current_hash text NOT NULL,
    new_hash text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    sends integer NOT NULL DEFAULT 1 CHECK (sends BETWEEN 1 AND 3),
    sent_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL
);
CREATE INDEX email_change_user_created_idx ON email_change(user_id,created_at);
-- +goose StatementBegin
CREATE FUNCTION settings_realtime_capture() RETURNS trigger LANGUAGE plpgsql SET search_path=public,pg_temp AS $$
BEGIN
    PERFORM realtime_emit('settings.changed','user',NEW.user_id);
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER settings_change AFTER INSERT OR UPDATE ON user_settings FOR EACH ROW EXECUTE FUNCTION settings_realtime_capture();
-- Separate INSERT-only signal; existing notification.changed remains compatible.
-- +goose StatementBegin
CREATE FUNCTION notification_created_capture() RETURNS trigger LANGUAGE plpgsql SET search_path=public,pg_temp AS $$
BEGIN
    IF NEW.channel='cabinet' THEN
        PERFORM realtime_emit('notification.created','user',NEW.user_id,NEW.id);
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_created AFTER INSERT ON notification FOR EACH ROW EXECUTE FUNCTION notification_created_capture();

-- +goose Down
UPDATE notification SET status='failed',last_error='settings_disabled' WHERE status='suppressed';
ALTER TABLE notification DROP CONSTRAINT notification_status_check;
ALTER TABLE notification ADD CONSTRAINT notification_status_check CHECK(status IN ('pending','sent','failed','read'));
DROP TRIGGER notification_created ON notification;
DROP FUNCTION notification_created_capture();
DROP TRIGGER settings_change ON user_settings;
DROP FUNCTION settings_realtime_capture();
DROP TABLE email_change;
DROP TABLE user_settings;
