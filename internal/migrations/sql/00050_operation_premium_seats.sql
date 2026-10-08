-- +goose Up
-- Preserve historical membership facts without inferring their seat class.
ALTER TABLE tsw_batch_memberships ADD COLUMN seat_type text
    CHECK (seat_type IS NULL OR seat_type IN ('default','prolite','usage_based','automation'));
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_platform_stage_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_platform_stage_ck
    CHECK (platform_request_stage IS NULL OR platform_request_stage IN ('send_invitation','request_join','accept_join'));

-- +goose Down
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_platform_stage_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_platform_stage_ck
    CHECK (platform_request_stage IS NULL OR platform_request_stage IN ('request_join','accept_join'));
ALTER TABLE tsw_batch_memberships DROP COLUMN seat_type;
