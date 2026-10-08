-- +goose Up
-- Invitation stages share this constraint with the existing removal worker.
-- Migration 50 omitted its request marker; retain every supported stage.
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_platform_stage_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_platform_stage_ck
    CHECK (platform_request_stage IS NULL OR platform_request_stage IN ('send_invitation','request_join','accept_join','remove_member'));

-- +goose Down
-- Constraint validation rejects rollback while removal markers still exist.
-- Never erase a marker for a platform request that may have taken effect.
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_platform_stage_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_platform_stage_ck
    CHECK (platform_request_stage IS NULL OR platform_request_stage IN ('send_invitation','request_join','accept_join'));
