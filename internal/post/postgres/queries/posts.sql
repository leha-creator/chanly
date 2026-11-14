-- Посты кампаний и черновики (04 §4). Выборки ограничены организацией (07 §4).

-- name: InsertPost :exec
INSERT INTO post (id, organization_id, campaign_id, status, draft, created_at, updated_at)
VALUES (@id, @organization_id, @campaign_id, @status, @draft, @created_at, @updated_at);

-- name: GetPostByCampaign :one
SELECT * FROM post WHERE campaign_id = @campaign_id AND organization_id = @organization_id;

-- name: GetPostByCampaignForUpdate :one
SELECT * FROM post WHERE campaign_id = @campaign_id AND organization_id = @organization_id FOR UPDATE;

-- name: SavePostDraft :execrows
UPDATE post SET draft = @draft, updated_at = @updated_at WHERE id = @id AND organization_id = @organization_id;

-- name: LockDraftMedia :exec
-- Lock before post/media rows: SaveDraft, CreateVersion and Delete use this order.
SELECT pg_advisory_xact_lock(hashtextextended('post-media:' || CAST(sqlc.arg(organization_id)::uuid AS text), 0));

-- name: RemoveMediaFromDrafts :many
-- R-15: файл убирается из черновиков всех постов организации, порядок остальных сохраняется.
UPDATE post SET
    draft = jsonb_set(draft, '{media_ids}', COALESCE(
        (SELECT jsonb_agg(e ORDER BY n) FROM jsonb_array_elements(draft -> 'media_ids') WITH ORDINALITY AS t(e, n)
         WHERE e <> to_jsonb(@file_id::text)),
        '[]'::jsonb)),
    updated_at = @now
WHERE organization_id = @organization_id AND draft -> 'media_ids' @> jsonb_build_array(@file_id::text)
RETURNING *;
