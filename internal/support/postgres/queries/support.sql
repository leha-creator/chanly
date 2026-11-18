-- name: SupportList :many
SELECT (to_jsonb(c)||jsonb_build_object(
 'unread',(SELECT count(*) FROM support_message m WHERE m.conversation_id=c.id AND m.direction='inbound' AND m.sequence>coalesce((SELECT r.sequence FROM support_read_marker r WHERE r.conversation_id=c.id AND r.actor_id=sqlc.arg(actor_id)::uuid),0)),
 'needs_reply',c.last_incoming>c.answered_through,
 'preview',coalesce((SELECT CASE WHEN m.deleted THEN '[Удалено]' WHEN m.text<>'' THEN left(m.text,160) ELSE '[Вложение]' END FROM support_message m WHERE m.conversation_id=c.id ORDER BY m.sequence DESC LIMIT 1),'')
 ))::jsonb AS data
FROM support_conversation c
WHERE (sqlc.arg(status)::text='' OR c.status=sqlc.arg(status))
 AND (NOT sqlc.arg(mine)::boolean OR c.assignee_id=sqlc.arg(actor_id))
 AND (NOT sqlc.arg(needs_reply)::boolean OR (c.status='open' AND c.last_incoming>c.answered_through))
 AND (sqlc.arg(query)::text='' OR c.name ILIKE '%'||sqlc.arg(query)||'%' OR c.username ILIKE '%'||sqlc.arg(query)||'%' OR c.max_user_id=sqlc.arg(query))
ORDER BY c.updated_at DESC,c.id DESC LIMIT sqlc.arg(page_limit)::int OFFSET sqlc.arg(page_offset)::int;

-- name: SupportTotal :one
SELECT count(*) FROM support_conversation c WHERE (sqlc.arg(status)::text='' OR c.status=sqlc.arg(status))
 AND (NOT sqlc.arg(mine)::boolean OR c.assignee_id=sqlc.arg(actor_id)::uuid)
 AND (NOT sqlc.arg(needs_reply)::boolean OR (c.status='open' AND c.last_incoming>c.answered_through))
 AND (sqlc.arg(query)::text='' OR c.name ILIKE '%'||sqlc.arg(query)||'%' OR c.username ILIKE '%'||sqlc.arg(query)||'%' OR c.max_user_id=sqlc.arg(query));

-- name: SupportGet :one
SELECT (to_jsonb(c)||jsonb_build_object('needs_reply',c.last_incoming>c.answered_through,'preview','',
 'unread',(SELECT count(*) FROM support_message m WHERE m.conversation_id=c.id AND m.direction='inbound' AND m.sequence>coalesce((SELECT r.sequence FROM support_read_marker r WHERE r.conversation_id=c.id AND r.actor_id=sqlc.arg(actor_id)::uuid),0))))::jsonb AS data
FROM support_conversation c WHERE c.id=sqlc.arg(id)::uuid;

-- name: SupportLock :one
SELECT id FROM support_conversation WHERE id=$1 FOR UPDATE;

-- name: SupportCreateConversation :one
INSERT INTO support_conversation(id,bot_id,max_user_id,name,username,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$6)
ON CONFLICT(bot_id,max_user_id) DO UPDATE SET name=excluded.name,username=excluded.username
RETURNING id;

-- name: SupportNextSequence :one
UPDATE support_conversation SET last_sequence=last_sequence+1,version=version+1,updated_at=$2 WHERE id=$1 RETURNING last_sequence;

-- name: SupportHistory :many
SELECT (to_jsonb(m)||jsonb_build_object('attachments',coalesce((SELECT jsonb_agg(to_jsonb(a)-'storage_key'-'provider_token'-'provider_url'-'actor_id'-'message_id') FROM support_attachment a WHERE a.message_id=m.id),'[]'::jsonb)))::jsonb AS data
FROM support_message m WHERE conversation_id=$1 AND (sqlc.arg(before_sequence)::bigint=0 OR sequence<sqlc.arg(before_sequence)) ORDER BY sequence DESC LIMIT sqlc.arg(page_limit)::int;

-- name: SupportMessage :one
SELECT (to_jsonb(m)||jsonb_build_object('attachments',coalesce((SELECT jsonb_agg(to_jsonb(a)-'storage_key'-'provider_token'-'provider_url'-'actor_id'-'message_id') FROM support_attachment a WHERE a.message_id=m.id),'[]'::jsonb)))::jsonb AS data
FROM support_message m WHERE m.id=$1;

-- name: SupportExistingReply :one
SELECT m.id,m.request_hash FROM support_message m WHERE m.conversation_id=$1 AND m.client_key=$2;

-- name: SupportInsertMessage :exec
INSERT INTO support_message(id,conversation_id,sequence,direction,text,actor_id,status,bot_id,external_id,client_key,request_hash,reply_to_sequence,event_at,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14);

-- name: SupportSetIncoming :exec
UPDATE support_conversation SET last_incoming=$2,status='open',updated_at=$3 WHERE id=$1;

-- name: SupportMarkRead :exec
INSERT INTO support_read_marker(conversation_id,actor_id,sequence)
SELECT c.id AS conversation_id,sqlc.arg(actor_id)::uuid AS actor_id,least(sqlc.arg(sequence)::bigint,c.last_sequence) AS sequence FROM support_conversation c WHERE c.id=sqlc.arg(conversation_id)::uuid
ON CONFLICT(conversation_id,actor_id) DO UPDATE SET sequence=greatest(support_read_marker.sequence,excluded.sequence);

-- name: SupportUpdate :execrows
UPDATE support_conversation SET status=$3,assignee_id=$4,version=version+1,updated_at=$5 WHERE id=$1 AND version=$2;

-- name: SupportAdminExists :one
SELECT EXISTS(SELECT 1 FROM "user" u WHERE u.id=$1 AND u.admin_role IN ('moderator','senior') AND u.blocked_at IS NULL)::boolean AS allowed;

-- name: SupportClaim :execrows
UPDATE support_message m SET status='sending',updated_at=$2 WHERE m.id=$1 AND m.status='queued'
AND NOT EXISTS(SELECT 1 FROM support_message p WHERE p.conversation_id=m.conversation_id AND p.sequence<m.sequence AND p.status IN ('queued','sending'));

-- name: SupportFinish :execrows
UPDATE support_message SET status=$2,external_id=$3,reason=$4,updated_at=$5 WHERE id=$1 AND status='sending';

-- name: SupportAnswered :exec
UPDATE support_conversation SET answered_through=greatest(answered_through,$2),version=version+1,updated_at=$3 WHERE id=$1;

-- name: SupportRetry :execrows
UPDATE support_message SET status='queued',reason='',updated_at=$2 WHERE id=$1 AND status='failed';

-- name: SupportStaleSending :many
UPDATE support_message SET status='unknown',reason='worker_interrupted',updated_at=$1 WHERE status='sending' AND updated_at<$2 RETURNING id;

-- name: SupportWaiting :one
SELECT count(*) FROM support_conversation WHERE status='open' AND last_incoming>answered_through;

-- name: SupportAttachment :one
SELECT * FROM support_attachment WHERE id=$1;

-- name: SupportSaveAttachment :exec
INSERT INTO support_attachment(id,message_id,actor_id,type,mime,size,name,status,duration,width,height,storage_key,provider_token,provider_url,original_key)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT(id) DO UPDATE SET status=excluded.status,mime=excluded.mime,size=excluded.size,storage_key=excluded.storage_key,original_key=excluded.original_key,provider_token=excluded.provider_token,provider_url=excluded.provider_url,duration=excluded.duration,name=excluded.name,width=excluded.width,height=excluded.height;

-- name: SupportClaimMedia :execrows
UPDATE support_attachment SET status='processing',processing_at=$2 WHERE id=$1 AND (status='pending' OR (status='processing' AND processing_at<$3));

-- name: SupportBindAttachment :execrows
UPDATE support_attachment SET message_id=$2 WHERE id=$1 AND message_id IS NULL AND actor_id=$3 AND status='ready';

-- name: SupportMessageAttachments :many
SELECT id FROM support_attachment WHERE message_id=$1 ORDER BY id;

-- name: SupportIncomingExists :one
SELECT id FROM support_message WHERE bot_id=$1 AND external_id=$2;

-- name: SupportRevise :exec
INSERT INTO support_revision(bot_id,external_id,text,deleted,event_at) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(bot_id,external_id) DO UPDATE SET text=excluded.text,deleted=excluded.deleted,event_at=excluded.event_at WHERE support_revision.event_at<excluded.event_at;

-- name: SupportApplyRevision :exec
UPDATE support_message m SET text=CASE WHEN r.deleted THEN '' ELSE r.text END,deleted=r.deleted,edited=NOT r.deleted,event_at=r.event_at
FROM support_revision r WHERE m.bot_id=r.bot_id AND m.external_id=r.external_id AND m.event_at<=r.event_at AND m.id=$1;

-- name: SupportCleanupCandidates :many
SELECT a.id,a.storage_key,a.original_key FROM support_attachment a LEFT JOIN support_message m ON m.id=a.message_id LEFT JOIN support_conversation c ON c.id=m.conversation_id
WHERE (a.message_id IS NULL AND a.created_at<$1) OR (c.status='closed' AND c.updated_at<$2 AND NOT EXISTS(SELECT 1 FROM support_message p WHERE p.conversation_id=c.id AND p.status IN ('queued','sending','unknown'))) LIMIT 1000;

-- name: SupportDeleteAttachment :exec
DELETE FROM support_attachment WHERE id=$1;

-- name: SupportDeleteClosed :execrows
DELETE FROM support_conversation c WHERE c.status='closed' AND c.updated_at<$1 AND NOT EXISTS(SELECT 1 FROM support_message m WHERE m.conversation_id=c.id AND (m.status IN ('queued','sending','unknown') OR EXISTS(SELECT 1 FROM support_attachment a WHERE a.message_id=m.id)));

-- name: SupportCleanupInbound :exec
UPDATE inbound_event SET payload='{}'::jsonb WHERE source='max_support' AND status IN ('processed','ignored') AND created_at<$1;

-- name: SupportRetireCandidates :many
SELECT c.id FROM support_conversation c WHERE c.status='closed' AND c.updated_at<$1
AND NOT EXISTS(SELECT 1 FROM support_message m WHERE m.conversation_id=c.id AND m.status IN ('queued','sending','unknown'))
ORDER BY c.id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: SupportRetireBlobs :exec
INSERT INTO support_blob_gc(storage_key) SELECT unnest(ARRAY[a.storage_key,a.original_key]) FROM support_attachment a JOIN support_message m ON m.id=a.message_id WHERE m.conversation_id=$1 ON CONFLICT DO NOTHING;

-- name: SupportRetireConversation :exec
DELETE FROM support_conversation WHERE id=$1;

-- name: SupportOrphans :many
SELECT a.id FROM support_attachment a WHERE a.message_id IS NULL AND a.created_at<$1 AND a.status<>'processing' ORDER BY a.id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: SupportRetireOrphanBlobs :exec
INSERT INTO support_blob_gc(storage_key) SELECT unnest(ARRAY[a.storage_key,a.original_key]) FROM support_attachment a WHERE a.id=$1 ON CONFLICT DO NOTHING;

-- name: SupportGC :many
SELECT storage_key FROM support_blob_gc LIMIT 1000;

-- name: SupportGCDone :exec
DELETE FROM support_blob_gc WHERE storage_key=$1;

-- name: SupportCleanupRevisions :exec
DELETE FROM support_revision WHERE event_at<$1;
