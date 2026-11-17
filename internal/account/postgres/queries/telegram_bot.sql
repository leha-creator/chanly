-- User-owned notification bot connection, independent of organizations and channels.

-- name: NotificationTelegramRecipient :one
SELECT b.telegram_user_id FROM user_notification_telegram_bot b
JOIN "user" u ON u.id=b.user_id
WHERE u.id=sqlc.arg(user_id) AND u.status='active';

-- name: CountTelegramBotConnections :one
SELECT count(*) FROM notification_telegram_bot_connection
WHERE user_id=sqlc.arg(user_id) AND created_at>sqlc.arg(since)::timestamptz;

-- name: ExpireTelegramBotConnections :exec
UPDATE notification_telegram_bot_connection SET status='expired'
WHERE user_id=sqlc.arg(user_id) AND status<>'expired';

-- name: InsertTelegramBotConnection :exec
INSERT INTO notification_telegram_bot_connection(id,user_id,token_hash,status,created_at,expires_at)
VALUES(sqlc.arg(id),sqlc.arg(user_id),sqlc.arg(token_hash),sqlc.arg(status),sqlc.arg(created_at),sqlc.arg(expires_at));

-- name: GetTelegramBotConnection :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_telegram_bot_connection WHERE user_id=sqlc.arg(user_id) AND id=sqlc.arg(id);

-- name: LockTelegramBotConnection :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_telegram_bot_connection WHERE user_id=sqlc.arg(user_id) AND id=sqlc.arg(id) FOR UPDATE;

-- name: GetTelegramBotConnectionByHash :one
SELECT id,user_id,token_hash,status,created_at,expires_at
FROM notification_telegram_bot_connection WHERE token_hash=sqlc.arg(token_hash);

-- name: SetNotificationTelegramRecipient :exec
INSERT INTO user_notification_telegram_bot(user_id,telegram_user_id,connected_at)
VALUES(sqlc.arg(user_id),sqlc.arg(telegram_user_id),sqlc.arg(connected_at))
ON CONFLICT(user_id) DO UPDATE SET telegram_user_id=EXCLUDED.telegram_user_id,connected_at=EXCLUDED.connected_at;

-- name: CompleteTelegramBotConnection :exec
UPDATE notification_telegram_bot_connection SET status='connected' WHERE id=sqlc.arg(id);

-- name: DisconnectNotificationTelegramBot :exec
DELETE FROM user_notification_telegram_bot WHERE user_id=sqlc.arg(user_id);
