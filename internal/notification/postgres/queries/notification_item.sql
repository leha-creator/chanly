-- name: GetOwnCabinetNotification :one
SELECT * FROM notification WHERE id = @id AND user_id = @user_id AND channel='cabinet';
