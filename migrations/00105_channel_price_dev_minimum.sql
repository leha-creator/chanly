-- The domain enforces the production floor; development permits positive kopecks.
-- +goose Up
ALTER TABLE channel_price DROP CONSTRAINT channel_price_amount_check;
ALTER TABLE channel_price ADD CONSTRAINT channel_price_amount_check CHECK (amount BETWEEN 1 AND 1000000000);

-- +goose Down
-- Refuse rollback while development prices below 100 RUB exist.
ALTER TABLE channel_price DROP CONSTRAINT channel_price_amount_check;
ALTER TABLE channel_price ADD CONSTRAINT channel_price_amount_check CHECK (amount BETWEEN 10000 AND 1000000000);
