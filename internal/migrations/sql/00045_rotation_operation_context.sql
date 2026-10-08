-- +goose Up
ALTER TABLE tsw_operation_selection_drafts
 ADD COLUMN rotation_from_batch_id uuid REFERENCES tsw_batches(id) ON DELETE RESTRICT;
-- +goose Down
ALTER TABLE tsw_operation_selection_drafts DROP COLUMN rotation_from_batch_id;
