-- +goose Up
ALTER TABLE tsw_operation_selection_drafts
 ADD COLUMN execution_batch_id uuid REFERENCES tsw_batches(id) ON DELETE RESTRICT,
 ADD COLUMN planned_at timestamptz;
ALTER TABLE tsw_batches
 ADD COLUMN source_standby_batch_id uuid REFERENCES tsw_standby_child_batches(id) ON DELETE RESTRICT,
 ADD COLUMN source_batch_name text,
 ADD COLUMN login_started_at timestamptz;
-- Existing login facts are retained. Only future starts require explicit authorization.
UPDATE tsw_batches b SET login_started_at=(
 SELECT min(t.created_at) FROM tsw_tasks t
 JOIN tsw_batch_memberships m ON m.id=t.membership_id
 WHERE m.batch_id=b.id AND t.task_type='oauth_generate'
) WHERE EXISTS(SELECT 1 FROM tsw_tasks t JOIN tsw_batch_memberships m ON m.id=t.membership_id WHERE m.batch_id=b.id AND t.task_type='oauth_generate');
-- +goose Down
ALTER TABLE tsw_batches DROP COLUMN login_started_at, DROP COLUMN source_batch_name, DROP COLUMN source_standby_batch_id;
ALTER TABLE tsw_operation_selection_drafts DROP COLUMN planned_at, DROP COLUMN execution_batch_id;
