-- +goose Up
ALTER TABLE inbound_event DROP CONSTRAINT inbound_event_source_check;
ALTER TABLE inbound_event ADD CONSTRAINT inbound_event_source_check CHECK(source IN ('max','rocketwork','telegram','max_support'));
ALTER TABLE outbound_request DROP CONSTRAINT outbound_request_system_check;
ALTER TABLE outbound_request ADD CONSTRAINT outbound_request_system_check CHECK(system IN ('max','yandex_ord','rocketwork','tbank','telegram','max_support'));
ALTER TABLE outbound_circuit DROP CONSTRAINT outbound_circuit_system_check;
ALTER TABLE outbound_circuit ADD CONSTRAINT outbound_circuit_system_check CHECK(system IN ('max','yandex_ord','rocketwork','tbank','telegram','max_support'));

CREATE TABLE support_conversation(
 id uuid PRIMARY KEY, bot_id text NOT NULL, max_user_id text NOT NULL,
 name text NOT NULL DEFAULT '', username text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
 assignee_id uuid REFERENCES "user"(id) ON DELETE SET NULL,
 version bigint NOT NULL DEFAULT 1, last_sequence bigint NOT NULL DEFAULT 0,
 last_incoming bigint NOT NULL DEFAULT 0, answered_through bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(bot_id,max_user_id)
);
CREATE INDEX support_conversation_queue_idx ON support_conversation(status,updated_at DESC,id DESC);
CREATE TABLE support_message(
 id uuid PRIMARY KEY, conversation_id uuid NOT NULL REFERENCES support_conversation(id) ON DELETE CASCADE,
 sequence bigint NOT NULL, direction text NOT NULL CHECK(direction IN ('inbound','outbound')),
 text text NOT NULL DEFAULT '', actor_id uuid REFERENCES "user"(id) ON DELETE SET NULL,
 status text NOT NULL CHECK(status IN ('queued','sending','sent','failed','unknown')),
 reason text NOT NULL DEFAULT '', bot_id text NOT NULL, external_id text NOT NULL DEFAULT '',
 client_key uuid, request_hash text NOT NULL DEFAULT '', reply_to_sequence bigint NOT NULL DEFAULT 0,
 edited boolean NOT NULL DEFAULT false, deleted boolean NOT NULL DEFAULT false,
 event_at timestamptz NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(conversation_id,sequence), UNIQUE(conversation_id,client_key)
);
CREATE UNIQUE INDEX support_message_mid_idx ON support_message(bot_id,external_id) WHERE external_id<>'';
CREATE INDEX support_message_send_idx ON support_message(conversation_id,sequence) WHERE status IN ('queued','sending');
CREATE TABLE support_read_marker(
 conversation_id uuid NOT NULL REFERENCES support_conversation(id) ON DELETE CASCADE,
 actor_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
 sequence bigint NOT NULL DEFAULT 0, PRIMARY KEY(conversation_id,actor_id)
);
CREATE TABLE support_attachment(
 id uuid PRIMARY KEY, message_id uuid REFERENCES support_message(id) ON DELETE CASCADE,
 actor_id uuid REFERENCES "user"(id) ON DELETE SET NULL,
 type text NOT NULL, mime text NOT NULL DEFAULT '', size bigint NOT NULL DEFAULT 0,
 name text NOT NULL DEFAULT '', status text NOT NULL, duration double precision NOT NULL DEFAULT 0,
 width integer NOT NULL DEFAULT 0,height integer NOT NULL DEFAULT 0,
 storage_key text NOT NULL DEFAULT '',original_key text NOT NULL DEFAULT '',provider_token text NOT NULL DEFAULT '',provider_url text NOT NULL DEFAULT '',
 processing_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX support_attachment_message_idx ON support_attachment(message_id);
CREATE TABLE support_revision(
 bot_id text NOT NULL, external_id text NOT NULL, text text NOT NULL,
 deleted boolean NOT NULL, event_at timestamptz NOT NULL,
 PRIMARY KEY(bot_id,external_id)
);

CREATE TABLE support_blob_gc(storage_key text PRIMARY KEY);

-- +goose Down
DROP TABLE support_blob_gc;
DROP TABLE support_revision;
DROP TABLE support_attachment;
DROP TABLE support_read_marker;
DROP TABLE support_message;
DROP TABLE support_conversation;
-- Do not remove shared integration CHECK values on rollback: retained events may reference them.
