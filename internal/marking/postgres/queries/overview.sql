-- Выборки по всем видам объектов ОРД: список ошибок S-20, зависшие (07 §3), метрика (07 §5.5).

-- name: ListOrdErrors :many
-- Ключевой курсор (updated_at, id) по объединению видов; пустой курсор — с начала.
SELECT e.object_type, e.id, e.ord_external_id, e.ord_status, e.sync_error, e.sync_attempts, e.updated_at,
    -- Последний вызов ОРД по объекту (outbound_request.object_type — операция адаптера).
    (SELECT r.id FROM outbound_request r
     WHERE r.system = 'yandex_ord'
       AND r.object_type = CASE e.object_type WHEN 'platform' THEN 'platforms' WHEN 'invoice_creatives' THEN 'invoice/creatives' ELSE e.object_type END
       AND r.object_id = e.ord_external_id
     ORDER BY r.created_at DESC LIMIT 1)::uuid AS last_request_id
FROM (
    SELECT 'organization'::text AS object_type, o.id, o.ord_external_id, o.ord_status, o.sync_error, o.sync_attempts, o.updated_at
    FROM ord_organization o WHERE o.sync_status = 'error'
    UNION ALL
    SELECT 'contract'::text, c.id, c.ord_external_id, c.ord_status, c.sync_error, c.sync_attempts, c.updated_at
    FROM ord_contract c WHERE c.sync_status = 'error'
    UNION ALL
    SELECT 'platform'::text, p.id, p.ord_external_id, p.ord_status, p.sync_error, p.sync_attempts, p.updated_at
    FROM ord_platform p WHERE p.sync_status = 'error'
    UNION ALL
    SELECT 'creative'::text, cr.id, cr.ord_external_id, cr.ord_status, cr.sync_error, cr.sync_attempts, cr.updated_at
    FROM creative cr WHERE cr.sync_status = 'error'
    UNION ALL
    SELECT 'statistics'::text, st.id, st.ord_external_id, st.ord_status, st.sync_error, st.sync_attempts, st.updated_at
    FROM ord_statistics st WHERE st.sync_status = 'error'
    UNION ALL
    SELECT 'invoice'::text, iv.id, iv.ord_external_id, iv.ord_status, iv.sync_error, iv.sync_attempts, iv.updated_at
    FROM ord_invoice iv WHERE iv.sync_status = 'error'
    UNION ALL
    SELECT 'invoice_creatives'::text, ic.ord_invoice_id, ic.ord_external_id, ic.ord_status, ic.sync_error, ic.sync_attempts, ic.updated_at
    FROM ord_invoice_creatives ic WHERE ic.sync_status = 'error'
) e
WHERE (@object_type::text = '' OR e.object_type = @object_type)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
   OR (e.updated_at, e.id, e.object_type) > (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid, @after_type::text))
ORDER BY e.updated_at, e.id, e.object_type
LIMIT @lim;

-- name: ListOrdStuckSent :many
SELECT s.object_type, s.id, s.sent_at FROM (
    SELECT 'organization'::text AS object_type, o.id, o.sent_at FROM ord_organization o WHERE o.sync_status = 'sent'
    UNION ALL
    SELECT 'contract'::text, c.id, c.sent_at FROM ord_contract c WHERE c.sync_status = 'sent'
    UNION ALL
    SELECT 'platform'::text, p.id, p.sent_at FROM ord_platform p WHERE p.sync_status = 'sent'
    UNION ALL
    SELECT 'creative'::text, cr.id, cr.sent_at FROM creative cr WHERE cr.sync_status = 'sent'
    UNION ALL
    SELECT 'statistics'::text, st.id, st.sent_at FROM ord_statistics st WHERE st.sync_status = 'sent'
    UNION ALL
    SELECT 'invoice'::text, iv.id, iv.sent_at FROM ord_invoice iv WHERE iv.sync_status = 'sent'
    UNION ALL
    SELECT 'invoice_creatives'::text, ic.ord_invoice_id, ic.sent_at FROM ord_invoice_creatives ic WHERE ic.sync_status = 'sent'
) s
WHERE s.sent_at < @before::timestamptz
ORDER BY s.sent_at, s.id
LIMIT @lim;

-- name: ListCreativesWithoutErid :many
-- T-11: креатив создан при одобрении версии; без erid дольше порога — версия ждёт.
SELECT * FROM creative WHERE erid IS NULL AND created_at < @before::timestamptz
ORDER BY created_at, id
LIMIT @lim;

-- name: CountOrdObjectsByStatus :many
SELECT 'organization'::text AS object_type, o.sync_status, count(*)::int AS n FROM ord_organization o GROUP BY o.sync_status
UNION ALL
SELECT 'contract'::text, c.sync_status, count(*)::int FROM ord_contract c GROUP BY c.sync_status
UNION ALL
SELECT 'platform'::text, p.sync_status, count(*)::int FROM ord_platform p GROUP BY p.sync_status
UNION ALL
SELECT 'creative'::text, cr.sync_status, count(*)::int FROM creative cr GROUP BY cr.sync_status
UNION ALL
SELECT 'statistics'::text, st.sync_status, count(*)::int FROM ord_statistics st GROUP BY st.sync_status
UNION ALL
SELECT 'invoice'::text, iv.sync_status, count(*)::int FROM ord_invoice iv GROUP BY iv.sync_status
UNION ALL
SELECT 'invoice_creatives'::text, ic.sync_status, count(*)::int FROM ord_invoice_creatives ic GROUP BY ic.sync_status;

-- name: LastOrdRequest :one
-- Последний вызов ОРД по объекту (этап «API администратора», R-17): тела — как сохранены адаптером.
SELECT r.id, r.operation, r.attempt, r.request, r.response_status, r.response_body, r.error, r.created_at
FROM outbound_request r
WHERE r.system = 'yandex_ord' AND r.object_type = @object_type AND r.object_id = @object_id
ORDER BY r.created_at DESC
LIMIT 1;
