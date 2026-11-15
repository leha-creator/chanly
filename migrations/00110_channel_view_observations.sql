-- +goose Up
-- Historical summaries cannot reconstruct past counter increments. Old rows stay unknown.
ALTER TABLE channel_metrics ADD COLUMN new_views bigint CHECK (new_views >= 0);
ALTER TABLE channel_metrics ADD COLUMN observed_at timestamptz;
ALTER TABLE channel_metrics ADD COLUMN views_period_started_at timestamptz;

CREATE TABLE channel_metrics_checkpoint (
    channel_id uuid PRIMARY KEY REFERENCES channel(id) ON DELETE CASCADE,
    observed_at timestamptz NOT NULL,
    complete boolean NOT NULL,
    day_invalid boolean NOT NULL
);
CREATE TABLE channel_message_view_counter (
    channel_id uuid NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    message_id text NOT NULL,
    views bigint NOT NULL CHECK (views >= 0),
    PRIMARY KEY (channel_id, message_id)
);

-- +goose Down
DROP TABLE channel_message_view_counter;
DROP TABLE channel_metrics_checkpoint;
ALTER TABLE channel_metrics DROP COLUMN views_period_started_at;
ALTER TABLE channel_metrics DROP COLUMN observed_at;
ALTER TABLE channel_metrics DROP COLUMN new_views;
