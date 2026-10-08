-- +goose Up
-- A workspace may serve independent account batches. Retry/idempotency remains
-- scoped to the original execution batch rather than reserving the whole space.
DROP INDEX tsw_operations_workspace_active_uq;
CREATE UNIQUE INDEX tsw_operations_batch_active_uq ON tsw_operations(batch_id,operation_type)
 WHERE status IN ('queued','running','awaiting_login','blocked');
DROP INDEX tsw_batches_active_binding_uq;
CREATE INDEX tsw_batches_binding_status_idx ON tsw_batches(binding_id,status);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Independent execution batches cannot be merged by rollback'; END $$;
-- +goose StatementEnd
