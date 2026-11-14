-- User-owned notification bot connection, independent of organizations and channels.

-- name: NotificationMAXRecipient :one
SELECT b.max_user_id FROM user_notification_max_bot b
JOIN "user" u ON u.id=b.user_id
WHERE u.id=sqlc.arg(user_id) AND u.status='active';

-- name: CountMAXBotConnections :one
SELECT count(*) FROM notification_max_bot_connection
WHERE user_id=sqlc.arg(user_id) AND created_at>sqlc.arg(since)::timestamptz;

-- name: ExpireMAXBotConnections :exec
UPDATE notification_max_bot_connection SET status='expired'
WHERE user_id=sqlc.arg(user_id) AND status<>'expired';

-- name: InsertMAXBotConnection :exec
INSERT INTO notification_max_bot_connection(id,user_id,token_hash,status,created_at,expires_at)
VALUES(sqlc.arg(id),sqlc.arg(user_id),sqlc.arg(token_hash),sqlc.arg(status),sqlc.arg(created_at),sqlc.arg(expires_at));

-- name: GetMAXBotConnection :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_max_bot_connection WHERE user_id=sqlc.arg(user_id) AND id=sqlc.arg(id);

-- name: LockMAXBotConnection :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_max_bot_connection WHERE user_id=sqlc.arg(user_id) AND id=sqlc.arg(id) FOR UPDATE;

-- name: GetMAXBotConnectionByHash :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_max_bot_connection WHERE token_hash=sqlc.arg(token_hash);

-- name: SetNotificationMAXRecipient :exec
INSERT INTO user_notification_max_bot(user_id,max_user_id,connected_at)
VALUES(sqlc.arg(user_id),sqlc.arg(max_user_id),sqlc.arg(connected_at))
ON CONFLICT(user_id) DO UPDATE SET max_user_id=EXCLUDED.max_user_id,connected_at=EXCLUDED.connected_at;

-- name: CompleteMAXBotConnection :exec
UPDATE notification_max_bot_connection SET status='connected' WHERE id=sqlc.arg(id);

-- name: DisconnectNotificationMAXBot :exec
DELETE FROM user_notification_max_bot WHERE user_id=sqlc.arg(user_id);
