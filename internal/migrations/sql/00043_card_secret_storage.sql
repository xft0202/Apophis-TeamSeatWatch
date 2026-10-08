-- +goose Up
-- Existing HMAC-only cards retain their history; their original secret cannot be recovered.
ALTER TABLE tsw_cards ADD COLUMN sealed_secret bytea;
ALTER TABLE tsw_cards ADD CONSTRAINT tsw_cards_sealed_secret_ck CHECK (sealed_secret IS NULL OR octet_length(sealed_secret) >= 43);
-- +goose Down
ALTER TABLE tsw_cards DROP COLUMN sealed_secret;
