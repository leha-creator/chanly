-- name: GetUserSettings :one
SELECT * FROM user_settings WHERE user_id = @user_id;

-- name: EnsureUserSettings :exec
INSERT INTO user_settings(user_id,updated_at) VALUES (@user_id,@now::timestamptz) ON CONFLICT DO NOTHING;

-- name: LockUserSettings :one
SELECT * FROM user_settings WHERE user_id = @user_id FOR UPDATE;

-- name: SaveUserSettings :exec
UPDATE user_settings SET version=version+1, hide_notification_preview= @hide_notification_preview,
quiet_hours= @quiet_hours, delivery= @delivery, updated_at= @now::timestamptz WHERE user_id= @user_id;
