-- +goose Up
ALTER TABLE channel_connection ADD COLUMN channel_name text;

-- +goose Down
ALTER TABLE channel_connection DROP COLUMN channel_name;
