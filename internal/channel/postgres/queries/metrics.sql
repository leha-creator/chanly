-- Суточные снимки метрик каналов (04 §3, S-32, R-19).

-- name: UpsertMetrics :exec
-- Повтор задания в те же сутки перезаписывает снимок (R-19).
INSERT INTO channel_metrics (id, channel_id, date, subscribers_count, avg_views, engagement_bp,
    posts_per_week, anomaly_flags, new_views, observed_at, views_period_started_at, created_at, updated_at)
VALUES (@id, @channel_id, @date, @subscribers_count, sqlc.narg(avg_views), sqlc.narg(engagement_bp),
    sqlc.narg(posts_per_week)::float8, @anomaly_flags, sqlc.narg(new_views)::bigint,
    sqlc.narg(observed_at)::timestamptz, sqlc.narg(views_period_started_at)::timestamptz, @now::timestamptz, @now::timestamptz)
ON CONFLICT (channel_id, date) DO UPDATE
SET subscribers_count = EXCLUDED.subscribers_count, avg_views = EXCLUDED.avg_views,
    engagement_bp = EXCLUDED.engagement_bp, posts_per_week = EXCLUDED.posts_per_week,
    anomaly_flags = EXCLUDED.anomaly_flags, new_views = EXCLUDED.new_views, observed_at = EXCLUDED.observed_at,
    views_period_started_at = EXCLUDED.views_period_started_at, updated_at = EXCLUDED.updated_at;

-- name: LastMetricsBefore :one
SELECT date, subscribers_count, avg_views, engagement_bp, posts_per_week, anomaly_flags, new_views, observed_at, views_period_started_at
FROM channel_metrics
WHERE channel_id = @channel_id AND date < @date
ORDER BY date DESC
LIMIT 1;

-- name: RecentMetrics :many
SELECT date, subscribers_count, avg_views, engagement_bp, posts_per_week, anomaly_flags, new_views, observed_at, views_period_started_at
FROM channel_metrics
WHERE channel_id = @channel_id
ORDER BY date DESC
LIMIT @lim;

-- name: GetMetricsCheckpoint :one
SELECT observed_at, complete, day_invalid FROM channel_metrics_checkpoint WHERE channel_id = @channel_id;

-- name: UpsertMetricsCheckpoint :exec
INSERT INTO channel_metrics_checkpoint(channel_id,observed_at,complete,day_invalid)
VALUES(@channel_id,@observed_at,@complete,@day_invalid)
ON CONFLICT(channel_id) DO UPDATE SET observed_at=EXCLUDED.observed_at,complete=EXCLUDED.complete,day_invalid=EXCLUDED.day_invalid;

-- name: ListMessageViewCounters :many
SELECT message_id, views FROM channel_message_view_counter WHERE channel_id = @channel_id;

-- name: DeleteMessageViewCounters :exec
DELETE FROM channel_message_view_counter WHERE channel_id = @channel_id;

-- name: InsertMessageViewCounters :exec
INSERT INTO channel_message_view_counter(channel_id,message_id,views)
SELECT @channel_id, unnest(@message_ids::text[]), unnest(@view_counts::bigint[]);
