-- +goose Up
-- Retain batch identity for membership and execution history after deletion.
ALTER TABLE tsw_standby_child_batches
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN deleted_by uuid,
 ADD CONSTRAINT tsw_standby_batch_deletion_actor CHECK ((deleted_at IS NULL) = (deleted_by IS NULL));
CREATE INDEX tsw_standby_child_batches_live_updated_idx
 ON tsw_standby_child_batches(updated_at DESC,id) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX tsw_standby_child_batches_live_updated_idx;
ALTER TABLE tsw_standby_child_batches
 DROP CONSTRAINT tsw_standby_batch_deletion_actor,
 DROP COLUMN deleted_by,
 DROP COLUMN deleted_at;
