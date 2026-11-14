-- Справочник категорий каналов (04 §3, A-14).

-- name: ListActiveCategories :many
SELECT id, slug, name, position FROM channel_category WHERE is_active ORDER BY position, slug;

-- name: ListAllCategories :many
SELECT id, slug, name, position, is_active FROM channel_category ORDER BY position, slug;

-- name: CategoryExistsActive :one
SELECT EXISTS (SELECT 1 FROM channel_category WHERE id = @id AND is_active);

-- name: InsertCategory :exec
-- Справочник ведёт senior (R-8 этапа «API администратора»).
INSERT INTO channel_category (id, slug, name, position, is_active, created_at, updated_at)
VALUES (@id, @slug, @name, @position, true, @created_at, @created_at);

-- name: NextCategoryPosition :one
SELECT (coalesce(max(position), 0) + 10)::int FROM channel_category;

-- name: GetCategoryForUpdate :one
SELECT * FROM channel_category WHERE id = @id FOR UPDATE;

-- name: UpdateCategory :exec
UPDATE channel_category SET name = @name, position = @position, is_active = @is_active, updated_at = @updated_at
WHERE id = @id;

-- name: CountChannelsInModeration :one
-- Счётчик раздела модерации каналов (R-16).
SELECT count(*)::int FROM channel WHERE status = 'moderation';
