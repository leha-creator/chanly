-- name: SettingsAccess :one
SELECT u.admin_role,
EXISTS(SELECT 1 FROM organization_member m JOIN advertiser_profile p ON p.organization_id=m.organization_id WHERE m.user_id=u.id) AS advertiser,
EXISTS(SELECT 1 FROM organization_member m JOIN blogger_profile p ON p.organization_id=m.organization_id WHERE m.user_id=u.id) AS blogger,
EXISTS(SELECT 1 FROM user_notification_max_bot b WHERE b.user_id=u.id) AS max_bot
FROM "user" u WHERE u.id= @user_id AND u.status='active';

-- name: ListOwnSessions :many
SELECT id,user_id,expires_at,user_agent,created_at,updated_at FROM session
WHERE user_id= @user_id AND revoked_at IS NULL AND expires_at> @now::timestamptz ORDER BY updated_at DESC,id;

-- name: RevokeOwnOtherSession :execrows
UPDATE session SET revoked_at= @now::timestamptz,updated_at= @now::timestamptz
WHERE user_id= @user_id AND id= @id AND id<> @current_id AND revoked_at IS NULL AND expires_at> @now::timestamptz;

-- name: RevokeOtherOwnSessions :execrows
UPDATE session SET revoked_at= @now::timestamptz,updated_at= @now::timestamptz
WHERE user_id= @user_id AND id<> @current_id AND revoked_at IS NULL;

-- name: CountEmailChanges :one
SELECT count(*) FROM email_change WHERE user_id= @user_id AND created_at> @since::timestamptz;

-- name: ConsumeEmailChanges :exec
UPDATE email_change SET consumed_at= @now::timestamptz WHERE user_id= @user_id AND consumed_at IS NULL;

-- name: InsertEmailChange :exec
INSERT INTO email_change(id,user_id,current_email,new_email,current_hash,new_hash,sent_at,expires_at,created_at)
VALUES (@id,@user_id,@current_email,@new_email,@current_hash,@new_hash,@sent_at,@expires_at,@sent_at);

-- name: LockEmailChange :one
SELECT * FROM email_change WHERE id= @id AND user_id= @user_id FOR UPDATE;

-- name: SaveEmailChange :exec
UPDATE email_change SET current_hash= @current_hash,new_hash= @new_hash,attempts= @attempts,sends= @sends,
sent_at= @sent_at,consumed_at= @consumed_at WHERE id= @id;

-- name: ChangeUserEmail :exec
UPDATE "user" SET email= @email,updated_at= @now::timestamptz WHERE id= @id;

-- name: LockAuthEmail :exec
SELECT pg_advisory_xact_lock(hashtextextended('auth_email:' || @email::text,0));

-- name: DeleteExpiredEmailChanges :execrows
DELETE FROM email_change WHERE id IN (SELECT id FROM email_change WHERE expires_at < @before::timestamptz ORDER BY expires_at LIMIT @batch);
