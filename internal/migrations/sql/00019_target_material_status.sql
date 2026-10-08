-- +goose Up
ALTER TABLE tsw_target_credentials ADD COLUMN material_status text NOT NULL DEFAULT 'needs_totp'
 CONSTRAINT tsw_target_credentials_material_status_ck CHECK (material_status IN ('complete', 'needs_totp'));
ALTER TABLE tsw_target_credentials ADD COLUMN materials_sealed boolean NOT NULL DEFAULT false;
-- Existing secrets are sealed at control startup with the deployment key before workers start.
-- Until then, all preexisting rows fail closed for login/join/delivery.

-- +goose Down
ALTER TABLE tsw_target_credentials DROP COLUMN materials_sealed;
ALTER TABLE tsw_target_credentials DROP COLUMN material_status;
