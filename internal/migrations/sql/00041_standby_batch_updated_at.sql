-- +goose Up
ALTER TABLE tsw_standby_child_batches ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
UPDATE tsw_standby_child_batches SET updated_at=created_at;
UPDATE tsw_standby_child_batches b SET updated_at=greatest(b.created_at,h.changed_at)
FROM (SELECT batch_id,max(changed_at) AS changed_at FROM (
 SELECT batch_id,changed_at FROM tsw_standby_child_history WHERE batch_id IS NOT NULL
 UNION ALL
 SELECT previous_batch_id,changed_at FROM tsw_standby_child_history WHERE previous_batch_id IS NOT NULL
) changes GROUP BY batch_id) h WHERE h.batch_id=b.id;
CREATE INDEX tsw_standby_child_batches_updated_idx ON tsw_standby_child_batches(updated_at DESC,id);

-- +goose Down
DROP INDEX tsw_standby_child_batches_updated_idx;
ALTER TABLE tsw_standby_child_batches DROP COLUMN updated_at;
