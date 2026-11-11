-- name: InsertChannelConnection :exec
INSERT INTO channel_connection(id, organization_id, user_id, token_hash, channel_name, expires_at, status, scan_id, created_at, updated_at)
VALUES (@id, @organization_id, @user_id, @token_hash, @channel_name, @expires_at, @status, @scan_id, @created_at, @updated_at);

-- name: ResolveChannelConnectionReference :many
SELECT id FROM channel WHERE CASE @kind::text
    WHEN 'title' THEN lower(btrim(coalesce(title,''))) = lower(@value::text)
    WHEN 'link' THEN rtrim(coalesce(link,''),'/') = @value::text
    WHEN 'id' THEN max_chat_id::text = @value::text
    ELSE false END
ORDER BY id LIMIT 2;

-- name: GetChannelConnection :one
SELECT * FROM channel_connection WHERE id = @id;

-- name: GetChannelConnectionForUpdate :one
SELECT * FROM channel_connection WHERE id = @id FOR UPDATE;

-- name: FindChannelConnectionTokenForUpdate :one
SELECT * FROM channel_connection WHERE token_hash = @token_hash FOR UPDATE;

-- name: SaveChannelConnection :exec
UPDATE channel_connection SET max_user_id = @max_user_id, token_used_at = @token_used_at,
    status = @status, reason = @reason, scan_id = @scan_id, channel_id = @channel_id, updated_at = @updated_at
WHERE id = @id;

-- name: CountChannelConnectionsSince :one
SELECT count(*) FROM channel_connection WHERE organization_id = @organization_id AND created_at >= @since::timestamptz;

-- name: LockChannelConnectionIssuance :exec
SELECT pg_advisory_xact_lock(hashtextextended(@organization_id::text, 8103));

-- name: ListDiscoveredChannelsForConnection :many
SELECT id FROM channel WHERE id > @after::uuid
    AND status NOT IN ('disconnected', 'blocked')
    AND (organization_id IS NULL OR organization_id = @organization_id)
ORDER BY id LIMIT @lim;

-- name: AddChannelConnectionCandidate :exec
INSERT INTO channel_connection_candidate(connection_id, channel_id) VALUES (@connection_id, @channel_id)
ON CONFLICT DO NOTHING;

-- name: ClearChannelConnectionCandidates :exec
DELETE FROM channel_connection_candidate WHERE connection_id = @connection_id;

-- name: ListChannelConnectionCandidates :many
SELECT c.id, coalesce(c.title, '')::text AS title, coalesce(c.link, '')::text AS link,
    coalesce(c.icon_url, '')::text AS icon_url
FROM channel_connection_candidate cc
JOIN channel c ON c.id = cc.channel_id
JOIN channel_connection s ON s.id = cc.connection_id
WHERE cc.connection_id = @connection_id AND c.status NOT IN ('disconnected', 'blocked')
    AND (c.organization_id IS NULL OR c.organization_id = s.organization_id)
ORDER BY c.title, c.id;

-- name: HasChannelConnectionCandidate :one
SELECT EXISTS(SELECT 1 FROM channel_connection_candidate WHERE connection_id = @connection_id AND channel_id = @channel_id);

-- name: DeleteExpiredChannelConnections :execrows
DELETE FROM channel_connection WHERE id IN (
    SELECT id FROM channel_connection WHERE expires_at < @before::timestamptz
    ORDER BY expires_at LIMIT @lim
);
