-- Уведомления (04 §8, R-4, R-10, R-12, R-16).

-- name: InsertNotification :execrows
INSERT INTO notification (id, user_id, organization_id, channel, template, payload, status, object_type, object_id,
    dedupe_key, max_user_id, telegram_user_id, attempts, last_error, sent_at, read_at, created_at, updated_at)
VALUES (@id, @user_id, @organization_id, @channel, @template, @payload, @status, @object_type, @object_id,
    @dedupe_key, @max_user_id, @telegram_user_id, @attempts, @last_error, @sent_at, @read_at, @created_at, @updated_at)
ON CONFLICT (user_id, channel, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING;

-- name: GetNotificationForUpdate :one
SELECT * FROM notification WHERE id = @id FOR UPDATE;

-- name: UpdateNotification :execrows
UPDATE notification SET status = @status, attempts = @attempts, last_error = @last_error, sent_at = @sent_at,
    read_at = @read_at, updated_at = @updated_at
WHERE id = @id;

-- name: ListCabinetNotifications :many
SELECT * FROM notification
WHERE user_id = @user_id AND channel = 'cabinet'
  AND (NOT @unread::bool OR read_at IS NULL)
  AND (NOT @has_cursor::bool OR (created_at, id) < (@cursor_at::timestamptz, @cursor_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @lim;

-- name: CountCabinetNotifications :one
SELECT count(*)::int AS n FROM notification
WHERE user_id = @user_id AND channel = 'cabinet' AND (NOT @unread::bool OR read_at IS NULL);

-- name: MarkNotificationRead :execrows
UPDATE notification SET status = 'read', read_at = COALESCE(read_at, @now), updated_at = @now
WHERE id = @id AND user_id = @user_id AND channel = 'cabinet';

-- name: MarkAllNotificationsRead :execrows
UPDATE notification SET status = 'read', read_at = @now, updated_at = @now
WHERE user_id = @user_id AND channel = 'cabinet' AND read_at IS NULL;

-- name: DeleteOldNotifications :execrows
DELETE FROM notification WHERE id IN (
    SELECT old.id FROM notification old WHERE old.status <> 'pending' AND old.created_at < @before
    ORDER BY old.created_at LIMIT @lim);

-- name: CountNotificationsByStatus :many
SELECT channel, status, count(*)::int AS n FROM notification GROUP BY channel, status ORDER BY channel, status;
