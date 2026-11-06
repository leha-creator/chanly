-- Данные ежемесячной отчётности ОРД (этап «API администратора и отчётность ОРД», R-19, R-20):
-- размещения с публикацией, пересекающей месяц, и живые публикации для снимка показов.

-- name: ReportPlacements :many
SELECT pl.id AS placement_id, pub.id AS publication_id, pl.channel_id, pl.post_version_id,
       o.organization_id AS advertiser_organization_id, pl.blogger_organization_id,
       pub.published_at, pub.removed_at, pub.views, pl.final_views,
       pl.total_amount, pl.refund_amount, pl.blogger_amount, pl.payout_amount, pl.fee_amount, pl.fee_charged
FROM placement pl
JOIN publication pub ON pub.placement_id = pl.id
JOIN "order" o ON o.id = pl.order_id
WHERE pub.published_at < @period_to::timestamptz
  AND (pub.removed_at IS NULL OR pub.removed_at >= @period_from::timestamptz)
ORDER BY pub.published_at, pl.id;

-- name: LivePublicationViews :many
SELECT id, views, views_checked_at FROM publication
WHERE removed_at IS NULL AND published_at < @at::timestamptz
ORDER BY id;
