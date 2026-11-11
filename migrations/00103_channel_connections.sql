-- +goose Up
CREATE TABLE channel_connection (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization(id),
    user_id uuid NOT NULL REFERENCES "user"(id),
    token_hash text NOT NULL UNIQUE,
    max_user_id bigint,
    token_used_at timestamptz,
    expires_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'checking', 'ready', 'connecting', 'connected', 'failed')),
    reason text NOT NULL DEFAULT '',
    scan_id uuid NOT NULL,
    channel_id uuid REFERENCES channel(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK ((max_user_id IS NULL) = (token_used_at IS NULL))
);
CREATE INDEX channel_connection_org_created_idx ON channel_connection(organization_id, created_at);
CREATE INDEX channel_connection_expires_idx ON channel_connection(expires_at);
CREATE TABLE channel_connection_candidate (
    connection_id uuid NOT NULL REFERENCES channel_connection(id) ON DELETE CASCADE,
    channel_id uuid NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    PRIMARY KEY (connection_id, channel_id)
);

-- +goose Down
DROP TABLE channel_connection_candidate;
DROP TABLE channel_connection;
