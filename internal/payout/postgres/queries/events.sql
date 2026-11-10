-- name: PrepareEventCommand :one
INSERT INTO rocketwork_event_command (id, environment, account_key, action, payload_hash, subscription_id)
VALUES (@id, @environment, @account_key, @action, @payload_hash, @subscription_id)
ON CONFLICT (environment, account_key, action, payload_hash) DO UPDATE SET updated_at = rocketwork_event_command.updated_at
RETURNING *;

-- name: ClaimEventCommand :execrows
UPDATE rocketwork_event_command SET state = 'in_flight', updated_at = now() WHERE id = @id AND state = 'prepared';

-- name: ResolveEventCommand :exec
UPDATE rocketwork_event_command SET state = 'confirmed', subscription_id = @subscription_id, updated_at = now() WHERE id = @id;

-- name: EventCursor :one
SELECT through_at FROM rocketwork_event_cursor WHERE environment = @environment AND account_key = @account_key;

-- name: AdvanceEventCursor :exec
INSERT INTO rocketwork_event_cursor (environment, account_key, through_at) VALUES (@environment, @account_key, @through_at)
ON CONFLICT (environment, account_key) DO UPDATE SET through_at = greatest(rocketwork_event_cursor.through_at, EXCLUDED.through_at);
