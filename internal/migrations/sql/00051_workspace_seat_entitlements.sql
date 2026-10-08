-- +goose Up
-- Retain the platform's explicit paid entitlement per seat class. Historical
-- reads remain unknown; membership totals never backfill subscription amounts.
ALTER TABLE tsw_workspace_verifications ADD COLUMN seat_entitlements jsonb NOT NULL
    DEFAULT '{}'::jsonb CHECK (jsonb_typeof(seat_entitlements) = 'object');

-- +goose Down
ALTER TABLE tsw_workspace_verifications DROP COLUMN seat_entitlements;
