-- +goose Up
-- A newer explicit connection test supersedes every older in-flight result.
ALTER TABLE tsw_delivery_destinations
    ADD COLUMN test_attempt bigint NOT NULL DEFAULT 0
        CONSTRAINT tsw_delivery_destinations_test_attempt_ck CHECK (test_attempt >= 0);

-- +goose Down
ALTER TABLE tsw_delivery_destinations DROP COLUMN test_attempt;
