-- name: GetPlacementForPayoutAct :one
SELECT p.blogger_organization_id, p.top_hours, p.duration_hours, o.number AS order_number,
       pub.url, pub.published_at, pub.removed_at
FROM placement p JOIN "order" o ON o.id = p.order_id
JOIN publication pub ON pub.placement_id = p.id
WHERE p.id = @id AND p.status = 'accepted';
