-- +goose Up
ALTER TABLE tsw_proxy_pool_settings ADD COLUMN routing_mode TEXT NOT NULL DEFAULT 'direct'
    CHECK (routing_mode IN ('direct', 'proxy_required'));
-- An explicitly selected supplier stays selected. Unconfigured installations
-- and saved manual-only pools adopt the default direct policy without losing records.
UPDATE tsw_proxy_pool_settings SET routing_mode='proxy_required'
WHERE EXISTS (SELECT 1 FROM tsw_proxy_pool_sources WHERE enabled);

-- +goose Down
ALTER TABLE tsw_proxy_pool_settings DROP COLUMN routing_mode;
