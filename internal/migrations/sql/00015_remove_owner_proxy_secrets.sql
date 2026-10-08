-- +goose Up
-- The prior Owner-editable proxy paths persisted deployment secrets in business
-- tables. Drop both entire paths, including all saved authentication material.
DROP TABLE IF EXISTS tsw_proxy_endpoints;
DROP TABLE IF EXISTS tsw_settings;

-- +goose Down
-- Secrets cannot safely be restored to a business database.
SELECT 1;
